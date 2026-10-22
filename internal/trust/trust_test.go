package trust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRememberAndTrusted(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "sub", FileName)}
	if s.Trusted("a", "h1") {
		t.Fatal("trusted before anything was remembered")
	}
	if err := s.Remember("a", "h1", "Talk A"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remember("b", "h2", "Talk B"); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted("a", "h1") || !s.Trusted("b", "h2") {
		t.Error("a remembered talk is not trusted")
	}
	if s.Trusted("a", "h2") || s.Trusted("c", "h1") {
		t.Error("a hash counts for another talk")
	}
	// A new version replaces the old one.
	if err := s.Remember("a", "h3", "Talk A"); err != nil {
		t.Fatal(err)
	}
	if s.Trusted("a", "h1") || !s.Trusted("a", "h3") {
		t.Error("the new version did not replace the old")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Path), "*.tmp")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestEmptyHashNeverTrusted(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), FileName)}
	if err := s.Remember("a", "", "T"); err == nil {
		t.Error("remembered an empty hash")
	}
	if s.Trusted("a", "") {
		t.Error("an empty hash is trusted")
	}
}

func TestBrokenFileIsEmpty(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), FileName)}
	if err := os.WriteFile(s.Path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.Trusted("a", "h") {
		t.Error("a broken file trusts something")
	}
	if err := s.Remember("a", "h", "T"); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted("a", "h") {
		t.Error("remembering over a broken file did not work")
	}
}
