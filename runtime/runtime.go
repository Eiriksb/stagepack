// Package runtime is the pinned Slidev runtime that Stagepack opens decks with.
//
// package.json pins it, and the embedded package-lock.json is the source of truth for its versions:
// pack compares a deck's Slidev against SlidevVersion, and leaves out of the deck what Lock already has.
// build/runtime.sh installs it into runtimes/<Major>/.
package runtime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed package-lock.json
var lockJSON []byte

var lock = parseLock(lockJSON)

func parseLock(data []byte) map[string]string {
	var l struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(data, &l); err != nil {
		panic(fmt.Sprintf("runtime: embedded package-lock.json: %v", err))
	}
	m := make(map[string]string)
	for key, p := range l.Packages {
		name, ok := strings.CutPrefix(key, "node_modules/")
		if !ok || strings.Contains(name, "/node_modules/") {
			continue // the root ("") or a nested copy
		}
		m[name] = p.Version
	}
	if m["@slidev/cli"] == "" {
		panic("runtime: embedded package-lock.json has no @slidev/cli")
	}
	return m
}

// Lock maps the runtime's top-level packages (node_modules/<name> in the lock) to their versions.
// The map is a copy; callers may change it.
func Lock() map[string]string {
	m := make(map[string]string, len(lock))
	for k, v := range lock {
		m[k] = v
	}
	return m
}

// SlidevVersion is the version of @slidev/cli in the runtime, e.g. "53.0.0".
func SlidevVersion() string { return lock["@slidev/cli"] }

// Major is SlidevVersion's major version; the runtime is installed in runtimes/<Major>/.
func Major() int {
	major, _, _ := strings.Cut(SlidevVersion(), ".")
	n, err := strconv.Atoi(major)
	if err != nil {
		panic(fmt.Sprintf("runtime: @slidev/cli version %q", SlidevVersion()))
	}
	return n
}
