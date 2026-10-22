package slidev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Headmatter is what Slidev reads from a talk's slides file to decide which
// code to load: the theme and the addons, as JSON values exactly as Slidev's
// parser produced them (null when not set).
type Headmatter struct {
	Theme  json.RawMessage `json:"theme"`
	Addons json.RawMessage `json:"addons"`
}

// headmatterScript runs Slidev's own parser on the slides file the way the CLI
// does when it starts (node/options.ts in @slidev/cli): the theme from the
// first slide's frontmatter, the addons from resolveConfig, which also reads
// "config:". It only reads the one file; no code from the talk runs.
const headmatterScript = `
import { readFile } from 'node:fs/promises'
import { pathToFileURL } from 'node:url'
const [core, file] = process.argv.slice(1)
const { parse, resolveConfig } = await import(pathToFileURL(core).href)
const md = await parse(await readFile(file, 'utf-8'), file)
const hm = { ...md.slides[0]?.frontmatter }
const cfg = resolveConfig(hm, undefined, file)
process.stdout.write(JSON.stringify({ theme: hm.theme ?? null, addons: cfg.addons ?? null }))
`

// headmatterTimeout bounds the parser run.
const headmatterTimeout = 60 * time.Second

// ReadHeadmatter runs the runtime's @slidev/parser (under runtimeDir, i.e.
// runtimes/<major>) with node on the slides file at slidesPath, and returns the
// theme and addons Slidev will load. It runs in runtimeDir, never in the talk's
// folder, and without NODE_OPTIONS, so nothing but the runtime's own code runs.
func ReadHeadmatter(ctx context.Context, node, runtimeDir, slidesPath string) (Headmatter, error) {
	ctx, cancel := context.WithTimeout(ctx, headmatterTimeout)
	defer cancel()
	core := filepath.Join(runtimeDir, "node_modules", "@slidev", "parser", "dist", "core.mjs")
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", headmatterScript, core, slidesPath)
	cmd.Dir = runtimeDir
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(kv), "NODE_OPTIONS=") { // names are case-insensitive on Windows
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	prepare(cmd)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 && len(msg) > 300 {
			msg = msg[i+1:]
		}
		return Headmatter{}, fmt.Errorf("read the headmatter: %v: %s", err, msg)
	}
	var h Headmatter
	if err := json.Unmarshal(stdout.Bytes(), &h); err != nil {
		return Headmatter{}, fmt.Errorf("read the headmatter: %w", err)
	}
	return h, nil
}
