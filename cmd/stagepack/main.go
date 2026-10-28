// Command stagepack packs a Slidev talk into one .sptx file (brand.Ext) and opens it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"stagepack/internal/app"
	"stagepack/internal/brand"
	"stagepack/internal/pack"
	"stagepack/internal/ui"
	"stagepack/internal/version"
)

var cmdName = strings.ToLower(brand.Name)

var usage = "usage: " + cmdName + " {pack|open|install|uninstall|version} [args]"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// goos is runtime.GOOS; tests change it.
var goos = runtime.GOOS

// openDeck is app.Open; tests replace it.
var openDeck = app.Open

func run(args []string, stdout, stderr io.Writer) int {
	// Windows starts the downloaded program with no arguments when it is double-clicked: install.
	if len(args) == 0 && goos == "windows" {
		args = []string{"install"}
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, brand.Name+": "+usage)
		return 2
	}
	// Windows starts the viewer with the deck as its only argument when a deck is double-clicked.
	if goos == "windows" && len(args) == 1 && strings.HasSuffix(strings.ToLower(args[0]), strings.ToLower(brand.Ext)) {
		args = []string{"open", args[0]}
	}
	switch args[0] {
	case "pack":
		return runPack(args[1:], stdout, stderr)
	case "open":
		return runOpen(args[1:], stdout, stderr)
	case "version", "--version":
		fmt.Fprintln(stdout, version.Version)
		return 0
	case "install", "uninstall":
		if goos != "windows" && goos != "linux" {
			fmt.Fprintf(stderr, "%s: %s is for Windows and Linux only\n", brand.Name, args[0])
			return 2
		}
		if args[0] == "install" {
			return runInstall(args[1:], stderr)
		}
		return runUninstall(args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "%s: unknown command %q\n%s\n", brand.Name, args[0], usage)
		return 2
	}
}

// listFlag collects a repeated flag.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

var packUsage = "usage: " + cmdName + " pack DIR -o OUT [--entry slides.md] [--title T] [--exclude PREFIX]... [--only-used-public] [--meta k=v]..."

func runPack(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, packUsage) }
	var out, entry, title string
	var exclude, meta listFlag
	var onlyUsed bool
	fs.StringVar(&out, "o", "", "output "+brand.Ext+" file")
	fs.StringVar(&entry, "entry", "slides.md", "slides file, relative to DIR")
	fs.StringVar(&title, "title", "", "talk title (default: title in the slides' headmatter, else the folder name)")
	fs.Var(&exclude, "exclude", "leave out this path prefix (relative to DIR); repeatable")
	fs.BoolVar(&onlyUsed, "only-used-public", false, "pack only the files in public/ the presentation names by path")
	fs.Var(&meta, "meta", "manifest metadata key=value; repeatable")

	// DIR may come before or after the flags.
	var dirs []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		dirs = append(dirs, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(dirs) != 1 || out == "" {
		fmt.Fprintln(stderr, packUsage)
		return 2
	}
	md := map[string]string{}
	for _, kv := range meta {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			fmt.Fprintf(stderr, "%s: --meta wants key=value, got %q\n", brand.Name, kv)
			return 2
		}
		md[k] = v
	}
	m, err := pack.Pack(pack.Options{Dir: dirs[0], Out: out, Entry: entry, Title: title, Exclude: exclude,
		OnlyUsedPublic: onlyUsed, Meta: md, Warn: stderr})
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", brand.Name, err)
		return 1
	}
	fmt.Fprintf(stdout, "packed %q (Slidev %s) into %s\n", m.Title, m.Slidev, out)
	return 0
}

var openUsage = "usage: " + cmdName + " open [--headless] [--home DIR] [--trust-code] [--default-browser] FILE"

// runOpen opens a deck and returns when it is stopped: 0 on a clean stop, 1 on
// an error (already shown to the user).
func runOpen(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, openUsage) }
	var headless, trustCode, defaultBrowser bool
	var home string
	fs.BoolVar(&headless, "headless", false, "no tray icon: status as JSON lines on stdout; stop with Ctrl-C or SIGTERM")
	fs.BoolVar(&trustCode, "trust-code", false, "run the talk's own code without asking (only for talks you trust)")
	fs.StringVar(&home, "home", "", "the viewer's folder (default: the folder of this program)")
	fs.BoolVar(&defaultBrowser, "default-browser", false, "open the talk in the default browser, not in windows of its own")
	var files []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		files = append(files, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	if len(files) != 1 {
		fmt.Fprintln(stderr, openUsage)
		return 2
	}
	if home == "" {
		exe, err := os.Executable()
		if err == nil {
			exe, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			fmt.Fprintf(stderr, "%s: find the viewer's folder: %v\n", brand.Name, err)
			return 1
		}
		home = filepath.Dir(exe)
	}
	defer startLog(home)()
	newUI := ui.NewTray
	if headless {
		newUI = func(a ui.Actions) ui.UI { return ui.NewHeadless(stdout, a) }
	}
	err := openDeck(context.Background(), app.Options{Home: home, DeckPath: files[0], UI: newUI, GOOS: goos, TrustCode: trustCode, DefaultBrowser: defaultBrowser})
	if errors.Is(err, app.ErrNotTrusted) {
		fmt.Fprintf(stderr, "%s: %v\n(where nobody can be asked, as with --headless, open a talk you trust with --trust-code)\n", brand.Name, err)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", brand.Name, err)
		return 1
	}
	return 0
}
