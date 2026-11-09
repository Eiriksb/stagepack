package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"stagepack/internal/brand"
	"stagepack/internal/serve"
)

type serveCall struct {
	o   serve.Options
	in  io.Reader
	out io.Writer
}

// fakeServe replaces serve.Run; it returns code.
func fakeServe(t *testing.T, code int) *[]serveCall {
	t.Helper()
	// No test sees this user's real install as a peer home.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	var got []serveCall
	old := serveDeck
	serveDeck = func(ctx context.Context, o serve.Options, in io.Reader, out io.Writer, d serve.Deps) int {
		got = append(got, serveCall{o, in, out})
		return code
	}
	t.Cleanup(func() { serveDeck = old })
	return &got
}

func TestServeCommand(t *testing.T) {
	got := fakeServe(t, 0)
	in := strings.NewReader(`{"cmd":"stop"}` + "\n")
	oldStdin := stdin
	stdin = in
	t.Cleanup(func() { stdin = oldStdin })
	var so, se bytes.Buffer
	deck := "x" + brand.Ext
	if code := run([]string{"serve", "--json", "--home", "/h", "--trust-code", "--addon", "/a", "--addon", "b", deck}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	if len(*got) != 1 {
		t.Fatalf("serve ran %d times", len(*got))
	}
	c := (*got)[0]
	want := serve.Options{Home: "/h", DeckPath: deck, TrustCode: true, Addons: []string{"/a", "b"}}
	if !reflect.DeepEqual(c.o, want) {
		t.Errorf("serve got %+v, want %+v", c.o, want)
	}
	if c.in != in || c.out != &so {
		t.Error("serve did not get stdin and stdout")
	}

	// FILE first; the default home is the program's folder; no addons, no --trust-code.
	if code := run([]string{"serve", deck, "--json"}, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if c := (*got)[1].o; c.Home != filepath.Dir(exe) || c.TrustCode || c.Addons != nil {
		t.Errorf("serve got %+v", c)
	}
}

func TestServeCommandPeerHomes(t *testing.T) {
	got := fakeServe(t, 0)
	def, err := defaultInstallHome()
	if err != nil {
		t.Fatal(err)
	}
	serveWith := func(args ...string) serve.Options {
		t.Helper()
		var so, se bytes.Buffer
		args = append([]string{"serve", "--json", "--home", "/h"}, append(args, "x"+brand.Ext)...)
		if code := run(args, &so, &se); code != 0 {
			t.Fatalf("exit %d: %s", code, se.String())
		}
		return (*got)[len(*got)-1].o
	}

	// No default install on this computer: only the folders named.
	if o := serveWith(); o.PeerHomes != nil {
		t.Errorf("peer homes = %q, want none: the default install's folder does not exist", o.PeerHomes)
	}
	if o := serveWith("--peer-home", "/p1", "--peer-home", "/p2"); !reflect.DeepEqual(o.PeerHomes, []string{"/p1", "/p2"}) {
		t.Errorf("peer homes = %q, want the two named", o.PeerHomes)
	}

	// With one, that comes first.
	if err := os.MkdirAll(def, 0o755); err != nil {
		t.Fatal(err)
	}
	if o := serveWith(); !reflect.DeepEqual(o.PeerHomes, []string{def}) {
		t.Errorf("peer homes = %q, want the default install %s", o.PeerHomes, def)
	}
	if o := serveWith("--peer-home", "/p1"); !reflect.DeepEqual(o.PeerHomes, []string{def, "/p1"}) {
		t.Errorf("peer homes = %q, want the default install, then /p1", o.PeerHomes)
	}
}

func TestServeCommandExitCodes(t *testing.T) {
	for _, code := range []int{0, 1, 3, 4} {
		fakeServe(t, code)
		var so, se bytes.Buffer
		if got := run([]string{"serve", "--json", "--home", "/h", "x" + brand.Ext}, &so, &se); got != code {
			t.Errorf("serve returned %d: exit %d", code, got)
		}
	}
}

func TestServeCommandUsage(t *testing.T) {
	got := fakeServe(t, 0)
	for _, args := range [][]string{
		{"serve"},
		{"serve", "--json"},
		{"serve", "x" + brand.Ext}, // JSON lines are the only way to talk to it
		{"serve", "--json", "a" + brand.Ext, "b" + brand.Ext},
		{"serve", "--json", "--bogus", "a" + brand.Ext},
	} {
		var so, se bytes.Buffer
		if code := run(args, &so, &se); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if so.Len() != 0 {
			t.Errorf("%v: stdout %q, want nothing: it carries only events", args, so.String())
		}
	}
	if len(*got) != 0 {
		t.Errorf("serve ran: %+v", *got)
	}
	var so, se bytes.Buffer
	run([]string{"bogus"}, &so, &se)
	if !strings.Contains(se.String(), "serve") {
		t.Errorf("the usage does not name serve: %q", se.String())
	}
}
