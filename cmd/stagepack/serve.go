package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"stagepack/internal/brand"
	"stagepack/internal/serve"
)

var serveUsage = "usage: " + cmdName + " serve --json [--home DIR] [--trust-code] [--addon DIR]... FILE"

// serveDeck is serve.Run; tests replace it.
var serveDeck = serve.Run

// stdin is where serve reads its commands; tests replace it.
var stdin io.Reader = os.Stdin

// runServe serves a deck for an editor until it is stopped: events as JSON lines on stdout,
// commands as JSON lines on stdin (see internal/serve). 0 stopped, 1 error, 2 usage, 3 not
// trusted, 4 busy. Stdout carries nothing but the events; the rest goes to stderr and the log.
func runServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, serveUsage) }
	var jsonLines, trustCode bool
	var home string
	var addons listFlag
	fs.BoolVar(&jsonLines, "json", false, "events as JSON lines on stdout, commands as JSON lines on stdin (required)")
	fs.BoolVar(&trustCode, "trust-code", false, "run the talk's own code without asking (only for talks you trust)")
	fs.StringVar(&home, "home", "", "the viewer's folder (default: the folder of this program)")
	fs.Var(&addons, "addon", "a Slidev addon folder to load on top of the talk's own (the editor's); repeatable")
	var files []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return serve.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(files) != 1 || !jsonLines {
		fmt.Fprintln(stderr, serveUsage)
		return serve.ExitUsage
	}
	if home == "" {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: find the viewer's folder: %v\n", brand.Name, err)
			return serve.ExitError
		}
		home = filepath.Dir(exe)
	}
	defer startLog(home)()
	// Ctrl-C or SIGTERM stops it as the end of stdin does. On Windows an editor closes stdin.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveDeck(ctx, serve.Options{Home: home, DeckPath: files[0], TrustCode: trustCode, Addons: addons}, stdin, stdout, serve.Deps{})
}
