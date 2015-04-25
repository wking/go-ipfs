package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	context "github.com/ipfs/go-ipfs/Godeps/_workspace/src/golang.org/x/net/context"
	assets "github.com/ipfs/go-ipfs/assets"
	cmds "github.com/ipfs/go-ipfs/commands"
	core "github.com/ipfs/go-ipfs/core"
	importer "github.com/ipfs/go-ipfs/importer"
	chunk "github.com/ipfs/go-ipfs/importer/chunk"
	merkledag "github.com/ipfs/go-ipfs/merkledag"
	namesys "github.com/ipfs/go-ipfs/namesys"
	config "github.com/ipfs/go-ipfs/repo/config"
	fsrepo "github.com/ipfs/go-ipfs/repo/fsrepo"
	unixfs "github.com/ipfs/go-ipfs/unixfs"
)

const nBitsForKeypairDefault = 2048

var initCmd = &cmds.Command{
	Helptext: cmds.HelpText{
		Tagline:          "Initializes IPFS config file",
		ShortDescription: "Initializes IPFS configuration files and generates a new keypair.",
	},

	Options: []cmds.Option{
		cmds.IntOption("bits", "b", "Number of bits to use in the generated RSA private key (defaults to 4096)"),
		cmds.BoolOption("force", "f", "Overwrite existing config (if it exists)"),

		// TODO need to decide whether to expose the override as a file or a
		// directory. That is: should we allow the user to also specify the
		// name of the file?
		// TODO cmds.StringOption("event-logs", "l", "Location for machine-readable event logs"),
	},
	PreRun: func(req cmds.Request) error {
		daemonLocked := fsrepo.LockedByOtherProcess(req.Context().ConfigRoot)

		log.Info("checking if daemon is running...")
		if daemonLocked {
			e := "ipfs daemon is running. please stop it to run this command"
			return cmds.ClientError(e)
		}

		return nil
	},
	Run: func(req cmds.Request, res cmds.Response) {

		force, _, err := req.Option("f").Bool() // if !found, it's okay force == false
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		nBitsForKeypair, bitsOptFound, err := req.Option("b").Int()
		if err != nil {
			res.SetError(err, cmds.ErrNormal)
			return
		}

		if !bitsOptFound {
			nBitsForKeypair = nBitsForKeypairDefault
		}

		rpipe, wpipe := io.Pipe()
		go func() {
			defer wpipe.Close()
			if err := doInit(wpipe, req.Context().ConfigRoot, force, nBitsForKeypair); err != nil {
				res.SetError(err, cmds.ErrNormal)
				return
			}
		}()
		res.SetOutput(rpipe)
	},
}

var errRepoExists = errors.New(`ipfs configuration file already exists!
Reinitializing would overwrite your keys.
(use -f to force overwrite)
`)

func initWithDefaults(out io.Writer, repoRoot string) error {
	err := doInit(out, repoRoot, false, nBitsForKeypairDefault)
	return err
}

func doInit(out io.Writer, repoRoot string, force bool, nBitsForKeypair int) error {
	if _, err := fmt.Fprintf(out, "initializing ipfs node at %s\n", repoRoot); err != nil {
		return err
	}

	if fsrepo.IsInitialized(repoRoot) && !force {
		return errRepoExists
	}

	conf, err := config.Init(out, nBitsForKeypair)
	if err != nil {
		return err
	}

	if fsrepo.IsInitialized(repoRoot) {
		if err := fsrepo.Remove(repoRoot); err != nil {
			return err
		}
	}

	if err := fsrepo.Init(repoRoot, conf); err != nil {
		return err
	}

	if err := addDefaultAssets(out, repoRoot); err != nil {
		return err
	}

	return initializeIpnsKeyspace(repoRoot)
}

func addDefaultAssets(out io.Writer, repoRoot string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := fsrepo.Open(repoRoot)
	if err != nil { // NB: repo is owned by the node
		return err
	}

	nd, err := core.NewIPFSNode(ctx, core.Offline(r))
	if err != nil {
		return err
	}
	defer nd.Close()

	dir := &merkledag.Node{Data: unixfs.FolderPBData()}

	// add every file in the assets pkg
	for fname, file := range assets.Init_dir {
		buf := bytes.NewBufferString(file)
		dagNode, err := importer.BuildDagFromReader(
			buf,
			nd.DAG,
			nd.Pinning.GetManual(),
			chunk.DefaultSplitter)
		if err != nil {
			return err
		}
		if err := dir.AddNodeLink(fname, dagNode); err != nil {
			return err
		}
	}

	dkey, err := nd.DAG.Add(dir)
	if err != nil {
		return err
	}

	if err := nd.Pinning.Pin(ctx, dir, true); err != nil {
		return err
	}

	if err := nd.Pinning.Flush(); err != nil {
		return err
	}

	if _, err = fmt.Fprintf(out, "to get started, enter:\n"); err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, "\n\tipfs cat /ipfs/%s/readme\n\n", dkey)
	return err
}

func initializeIpnsKeyspace(repoRoot string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r, err := fsrepo.Open(repoRoot)
	if err != nil { // NB: repo is owned by the node
		return err
	}

	nd, err := core.NewIPFSNode(ctx, core.Offline(r))
	if err != nil {
		return err
	}
	defer nd.Close()

	err = nd.SetupOfflineRouting()
	if err != nil {
		return err
	}

	return namesys.InitializeKeyspace(ctx, nd.DAG, nd.Namesys, nd.Pinning, nd.PrivateKey)
}
