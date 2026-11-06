# Export to PowerPoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Right-click a `.sptx` on Windows or Linux and get an editable or an animated `.pptx` next to it, with a progress box, and nothing to install beyond Stagepack.

**Architecture:** slidev-pptx-export gains a browser path, an ffmpeg path, JSON progress events and an atomic write (Part A, its own PR on top of its PR #4). Stagepack bundles that exporter, Playwright's headless Chromium and a GPL ffmpeg, adds `stagepack export-pptx` (unpack, ask, run, report), a progress box and a finished box on both platforms, and registers two right-click entries (Part B).

**Tech Stack:** Go 1.27 (Stagepack), Node 24 ES modules with `node:test` (exporter), `playwright-core`, Playwright's `chromium-headless-shell`, ffmpeg with libx264, zenity/kdialog, Win32 Task Dialog through `golang.org/x/sys/windows`.

**Spec:** `docs/superpowers/specs/2026-11-06-export-to-powerpoint-design.md` (Stagepack repository).

Repositories:
- **Exporter:** `/home/bjoeir/Documents/Projects/Research/slidev-pptx-export`. Part A works in a new worktree on a branch `claude/stagepack-export` cut from `claude/funny-elbakyan-12961d` (PR #4's branch).
- **Stagepack:** `/home/bjoeir/Documents/Projects/stagepack/.claude/worktrees/export-pptx`, branch `claude/export-pptx`.

## Global Constraints

- Platforms: Windows x64 and Linux x86_64 (GNOME and KDE). No macOS.
- Menu wording, exactly: `Export to PowerPoint (editable)` and `Export to PowerPoint (with animations)`.
- Output: `<name>.pptx` (editable) or `<name>-animations.pptx`, next to the `.sptx`, where `<name>` is the file name without `.sptx`.
- Editable is the exporter's render mode (`--render`); animated is `--animations`.
- Nothing installed for the person exporting beyond Stagepack; no download at export time.
- No `.pptx` left half-written after Cancel or a failure; the work folder is removed as when a presentation closes.
- A presentation with code is not run without Stagepack's "contains code" question: same text, same trust store, same remembered answer as opening.
- Licence policy (README "Licences", reworded in Task B1): the viewer and what it links stay permissive (with the existing weak-copyleft exceptions); separate programs it runs as their own processes (Node, the headless browser, ffmpeg) may be GPL or LGPL, shipped unmodified, each with its licence text in the third-party licence file and its exact source in Stagepack's release assets.
- Exporter environment variables: `SLIDEV_PPTX_BROWSER` (browser executable) and `SLIDEV_PPTX_FFMPEG` (ffmpeg program); config keys `render.browser` and `render.ffmpeg` win over them.
- Exporter progress: `--progress json` writes one JSON object per line to stdout and nothing else there; human-readable notices stay on stderr.
- Exporter commits end with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`. The same in Stagepack.
- Never name the organisation's test deck in either repository, commits or PRs.

## Review Focus

- A `.sptx` whose path has spaces, quotes, `$` or non-ASCII letters exports from every menu entry with the path intact. Pinned in Task B8.
- A `.sptx` in a folder that can't be written (a mounted share, a read-only download folder) asks where to save instead of failing. Pinned in Task B6.
- Cancel at any moment, including while Slidev starts or a click is recorded, leaves no `.partial`, no work folder and no Slidev, browser or ffmpeg process. Pinned in Tasks B3 and B6.
- An exporter that fails part-way (a slide error, a crash) gives one sentence and the log's location, and no file. Pinned in Task B6.
- Two exports of different files run together; the same file twice is refused while the first runs, and a lock left by a killed export doesn't block the next. Pinned in Task B6.

---

## Part A: slidev-pptx-export

### Task A1: Use playwright-core with a named browser

**Files:**
- Modify: `src/render/browser.mjs`, `src/render/index.mjs` (the launch in `renderSpecs`)
- Test: `test/browser.test.mjs` (new), `test/render-index.test.mjs`

**Interfaces:**
- Produces: `loadChromium(deckDir, { resolve, executablePath } = {}) -> chromium`; `renderSpecs` launches with `{ executablePath }` when `config.render.browser ?? process.env.SLIDEV_PPTX_BROWSER` is set, and passes `{ executablePath }` as `chromiumFor`'s second argument.

- [ ] **Step 1: Write the failing tests**

```js
const fake = (present) => (name) => (present.includes(name) ? `/fake/${name}/index.js` : null);
// loadChromium imports the resolved path; inject an importer too: loadChromium(dir, { resolve, executablePath, importer })
test("playwright-chromium still comes first", async () => { /* resolve finds all three; the chromium from playwright-chromium is returned */ });
test("playwright-core alone is used when a browser is named", async () => { /* only playwright-core; executablePath "/opt/shell" → its chromium */ });
test("playwright-core alone with no browser named is an error that names render.browser", async () => {
  await assert.rejects(loadChromium("/d", { resolve: fake(["playwright-core"]), importer }), /render\.browser|SLIDEV_PPTX_BROWSER/);
});
```

In `test/render-index.test.mjs`: `launch` receives `{ executablePath: "/opt/shell" }` when `userConfig.render.browser` is `/opt/shell`, and also when only `process.env.SLIDEV_PPTX_BROWSER` is (restore the variable after); without either, `launch` gets no `executablePath`.

- [ ] **Step 2: Run them to verify they fail**

Run: `node --test test/browser.test.mjs test/render-index.test.mjs`
Expected: FAIL (no `importer` option, playwright-core not tried, `launch` called without options).

- [ ] **Step 3: Implement** — add `playwright-core` after `playwright` in the search; accept it only when `executablePath` is set; add an `importer = (p) => import(pathToFileURL(p).href)` option for tests. In `renderSpecs`, read the path once and pass it to `chromiumFor` and `launch`.

- [ ] **Step 4: Run** `npm test` — Expected: PASS.

- [ ] **Step 5: Commit** — `Render mode: drive a named browser through playwright-core`.

### Task A2: One ffmpeg path for every use of ffmpeg

**Files:**
- Modify: `src/render/encode.mjs`, `src/render/animate.mjs`, `src/render/index.mjs`, `src/render/click-states.mjs`
- Test: `test/encode.test.mjs`, `test/animate.test.mjs`, `test/render-index.test.mjs`

**Interfaces:**
- Produces: `findEncoder({ run, ffmpeg } = {}) -> { kind: "ffmpeg", bin } | { kind: "gstreamer" } | null`; `encoderArgs(kind, { dir, count, fps, out, bin = "ffmpeg" })`; `encodeClip` uses `encoder.bin`; `hasFfmpeg(bin)` caches per `bin`; `renderSpecs` passes `config.render.ffmpeg ?? process.env.SLIDEV_PPTX_FFMPEG` to `findEncoder` (via `captureClickStates`'s `encoder`) and to `animateVideos` as the `bin` of `videoGif`, `frameRate` and `hasFfmpeg`.

- [ ] **Step 1: Write the failing tests**

```js
test("a named ffmpeg with libx264 is used before the one on PATH", () => {
  const seen = [];
  const run = (bin, args) => { seen.push(bin); return { status: 0, stdout: " V..... libx264 " }; };
  assert.deepEqual(findEncoder({ run, ffmpeg: "/opt/ff/ffmpeg" }), { kind: "ffmpeg", bin: "/opt/ff/ffmpeg" });
  assert.equal(seen[0], "/opt/ff/ffmpeg");
});
test("ffmpeg's arguments start with the named program", () => {
  assert.equal(encoderArgs("ffmpeg", { dir: "/d", count: 3, fps: 30, out: "/o.mp4", bin: "/opt/ff/ffmpeg" }).bin, "/opt/ff/ffmpeg");
});
test("whether ffmpeg is there is remembered per program, not once for all", () => { /* hasFfmpeg("/nope") false, hasFfmpeg(process.execPath) true */ });
```

In `test/render-index.test.mjs`: with `render: { ffmpeg: "/opt/ff/ffmpeg" }` the default flow's `deps.gif` receives `bin: "/opt/ff/ffmpeg"`; the animated flow's `captureStates` receives an `encoder` built from it (inject `findEncoder` through `deps.findEncoder`).

- [ ] **Step 2: Run them to verify they fail** — `node --test test/encode.test.mjs test/animate.test.mjs test/render-index.test.mjs`. Expected: FAIL.

- [ ] **Step 3: Implement** as the Interfaces block says. The existing results gain `bin` (`{ kind: "ffmpeg", bin: "ffmpeg" }` from PATH); update the existing tests that compare whole objects.

- [ ] **Step 4: Run** `npm test && npm run test:render` — Expected: PASS.

- [ ] **Step 5: Commit** — `Render mode: render.ffmpeg names the ffmpeg for clips and animated pictures alike`.

### Task A3: Progress events and `--progress json`

**Files:**
- Modify: `src/index.mjs` (`convert`), `src/render/index.mjs`, `src/render/capture.mjs` (`captureDeck`), `src/render/click-states.mjs`, `bin/cli.mjs`
- Test: `test/render/click-states.test.mjs`, `test/render/capture.test.mjs`, `test/render/animations.test.mjs`

**Interfaces:**
- Produces: `convert({ ..., onProgress })`; `renderSpecs({ ..., onProgress })`; `captureDeck({ ..., onProgress })`; `captureClickStates({ ..., onProgress })`. `onProgress(event)` receives exactly these objects:
  - `{ event: "start", slides }` once the deck is open
  - `{ event: "slide", slide, states }` once a slide's number of states is known (`1` in the default capture)
  - `{ event: "state", slide, state }` after each state is captured (`state` counts from 0)
  - `{ event: "record", slide, click }` before a click is recorded
  - `{ event: "clip", slide, click, seconds, bytes }` after a clip is made
  - `{ event: "notice", text }` for each `Notice:` or `Warning:` line
  - `{ event: "write" }` before the file is written
  - `{ event: "done", path, slides, clips, bytes }` after it is written (`slides` in the `.pptx`)
  - `{ event: "error", message }` when the export fails (the CLI's last line, exit code 1)
- CLI: `--progress json` sends these as JSON lines to stdout; `console.warn` lines still go to stderr and are also sent as `notice` events.

- [ ] **Step 1: Write the failing tests**

In `click-states.test.mjs`, for `TO_WHITE` with a fake recording: the events are, in order, `start {slides:1}`, `slide {slide:1, states:2}`, `state 0`, `state 1`, `record {click:1}`, `clip {click:1, seconds:…, bytes:3}`, and a `notice` whose text starts `Notice: slide 1 click 1: clip`. In `capture.test.mjs`: the default capture emits `start`, then `slide {states:1}` and `state {state:0}` per slide. In `animations.test.mjs`: the CLI with `--animations --progress json` writes only JSON lines to stdout; the first is `start`, the last `done` with `path` equal to the output and `slides` 4 (3 without an encoder); a deck path that doesn't exist ends with an `error` line and exit code 1.

- [ ] **Step 2: Run them to verify they fail** — `node --test test/render/click-states.test.mjs test/render/capture.test.mjs test/render/animations.test.mjs`. Expected: FAIL.

- [ ] **Step 3: Implement** — thread `onProgress = () => {}` through; the CLI's json mode wraps `console.warn` to also emit `notice`, prints nothing else to stdout, and turns a thrown error into the `error` line. `convert`'s result gains `clips` (motion clicks exported as clips) and `bytes`.

- [ ] **Step 4: Run** `npm test && npm run test:render` — Expected: PASS.

- [ ] **Step 5: Commit** — `Export: --progress json reports each step as a JSON line`.

### Task A4: Write the .pptx atomically

**Files:**
- Create: `src/write.mjs`
- Modify: `src/index.mjs` (`convert` writes through it)
- Test: `test/write.test.mjs` (new), `test/convert.test.mjs`

**Interfaces:**
- Produces: `writeAtomic(path, data, { fs = defaultFs } = {})`, where `defaultFs` is `{ writeFile, rename, rm }` from `node:fs/promises`: writes `<path>.partial`, renames it to `path`, and on any error removes `<path>.partial` and rethrows. `convert` uses it for the `.pptx`.

- [ ] **Step 1: Write the failing tests**

```js
test("the file appears under its name only once it is complete", async (t) => {
  const calls = [];
  const fs = { writeFile: async (p) => calls.push(["write", p]), rename: async (a, b) => calls.push(["rename", a, b]), rm: async () => {} };
  await writeAtomic("/d/out.pptx", Buffer.from("x"), { fs });
  assert.deepEqual(calls, [["write", "/d/out.pptx.partial"], ["rename", "/d/out.pptx.partial", "/d/out.pptx"]]);
});
test("a write that fails leaves no .partial behind", async (t) => {
  const removed = [];
  const fs = { writeFile: async () => {}, rename: async () => { throw new Error("EXDEV"); }, rm: async (p) => removed.push(p) };
  await assert.rejects(writeAtomic("/d/out.pptx", Buffer.from("x"), { fs }), /EXDEV/);
  assert.deepEqual(removed, ["/d/out.pptx.partial"]);
});
```

In `test/convert.test.mjs`: after converting the kitchen-sink fixture into an empty temp folder, the folder holds exactly the `.pptx`.

- [ ] **Step 2: Run them to verify they fail** — `node --test test/write.test.mjs test/convert.test.mjs`. Expected: FAIL, `src/write.mjs` not found.

- [ ] **Step 3: Implement** `src/write.mjs` and use it in `convert`.

- [ ] **Step 4: Run** `npm test` — Expected: PASS.

- [ ] **Step 5: Commit** — `Export: write the .pptx under a temporary name and rename it when done`.

### Task A5: Exporter README and its PR

**Files:**
- Modify: `README.md`, `bin/cli.mjs` (usage comment)

- [ ] **Step 1:** README: `render.browser`, `render.ffmpeg`, their environment variables, `--progress json` with the event list from Task A3, and a short "From a .sptx" section: Stagepack's installers bring the exporter, a browser and ffmpeg; right-click a `.sptx`. CLI header comment lists `--progress json`.
- [ ] **Step 2:** Run `npm test && npm run test:render`. Expected: PASS, fixture tests run.
- [ ] **Step 3:** Commit `README: the options Stagepack uses, and exporting from a .sptx`; push `claude/stagepack-export`; open a PR with base `claude/funny-elbakyan-12961d` (PR #4). Body: what each change is for, that Stagepack consumes them, what was tested.

---

## Part B: Stagepack

### Task B1: Bundle the headless browser and ffmpeg, with the licence policy reworded

**Files:**
- Modify: `build/versions.env`, `build/fetch.sh`, `Makefile`, `build/payload.sh`, `build/release.sh`, `README.md` (Licences, Layout, Install), `.gitignore` if needed
- Create: `build/programs.tsv`, `build/programs.sh`

**Interfaces:**
- Produces: in `dist/<linux|windows>/`: `browser-shell/` (Playwright's `chromium-headless-shell` for the revision the pinned `playwright-core` expects; executable `browser-shell/chrome-headless-shell` or `browser-shell\chrome-headless-shell.exe`) and `ffmpeg/ffmpeg[.exe]`; the same paths in the payload and the install folder. Make targets `programs OS=linux|win32` (folded into `build-linux`, `build-windows`, `setup`).

- [ ] **Step 1:** Pin in `build/versions.env`: `PLAYWRIGHT_VERSION`, `CHROMIUM_SHELL_REVISION`, `CHROMIUM_SHELL_SHA256_LINUX_X64`, `CHROMIUM_SHELL_SHA256_WIN_X64`, `FFMPEG_BUILD` (a dated, static, GPL, libx264 build of ffmpeg 7.x for linux64 and win64 from BtbN's FFmpeg-Builds releases), `FFMPEG_SHA256_LINUX_X64`, `FFMPEG_SHA256_WIN_X64`, and the URLs of the matching source archives. Record each value's origin in a comment.
- [ ] **Step 2:** `build/fetch.sh` gains `shell-linux`, `shell-win`, `ffmpeg-linux`, `ffmpeg-win`, verifying checksums as for Node; the Makefile's `programs` target unpacks them into `dist/<os>/`.
- [ ] **Step 3:** `build/programs.tsv` lists each bundled program (Node, chromium-headless-shell, ffmpeg): name, version, licence, licence-file path, source archive URL. `build/programs.sh dist/<os>` fails when a program in `dist/<os>` is missing from the list, its licence file is missing, or its source archive URL is empty, and appends their licence texts to the third-party licence file. `make build-linux` and `build-windows` run it.
- [ ] **Step 4:** `build/payload.sh` zips `browser-shell` and `ffmpeg`; `build/release.sh` uploads the binaries' and sources' archives as release assets.
- [ ] **Step 5:** README: the reworded policy (Global Constraints), a table row per bundled program, the Layout list (`browser-shell/`, `ffmpeg/`), and the download sizes measured from the built tarball and setup.
- [ ] **Step 6:** Run `make build-linux && build/programs.sh dist/linux && ls dist/linux/browser-shell dist/linux/ffmpeg`. Expected: both present, the check passes; `dist/linux/ffmpeg/ffmpeg -hide_banner -encoders | grep libx264` prints a line; `dist/linux/browser-shell/chrome-headless-shell --version` prints a version. Run `make build-windows` too. Expected: both present.
- [ ] **Step 7:** Commit `Build: bundle Playwright's headless Chromium and ffmpeg, and reword the licence policy for separate programs`.

### Task B2: The exporter in the runtime

**Files:**
- Modify: `runtime/package.json`, `runtime/package-lock.json`
- Create: `runtime/vendor/slidev-pptx-export-<version>-dev.tgz` (removed again in Task B12)

**Interfaces:**
- Consumes: Part A's branch, packed with `npm pack`.
- Produces: `runtimes/53/node_modules/slidev-pptx-export/bin/cli.mjs` and `runtimes/53/node_modules/playwright-core`, at `PLAYWRIGHT_VERSION`.

- [ ] **Step 1:** Add `"slidev-pptx-export": "file:vendor/slidev-pptx-export-<version>-dev.tgz"` and `"playwright-core": "<PLAYWRIGHT_VERSION>"` (exact); `npm install --package-lock-only` in `runtime/`.
- [ ] **Step 2:** Run `make runtime OS=linux`. Expected: `build/licenses.mjs` passes, and `THIRD_PARTY_LICENSES.txt` lists both packages and their dependencies.
- [ ] **Step 3:** Commit `Runtime: slidev-pptx-export and playwright-core (development build of the exporter)`.

### Task B3: Run the exporter

**Files:**
- Create: `internal/export/run.go`, `internal/export/proc_linux.go`, `internal/export/proc_windows.go`, `internal/export/run_test.go`

**Interfaces:**
- Produces:
  - `type Paths struct{ Node, CLI, Browser, FFmpeg string }` and `func PathsFor(home, goos string) Paths` (`node/node[.exe]`, `runtimes/<major>/node_modules/slidev-pptx-export/bin/cli.mjs`, `browser-shell/chrome-headless-shell[.exe]`, `ffmpeg/ffmpeg[.exe]`).
  - `type Job struct{ Slides string /* the slides file in the work folder */; Out string; Animations bool }`
  - `type Event struct{ Event string; Slides, Slide, States, State, Click, Clips int; Seconds float64; Bytes int64; Path, Text, Message string }` with JSON tags matching Task A3.
  - `func Command(p Paths, j Job) *exec.Cmd`: `node <CLI> <Slides> <Out> --render [--animations] --progress json`, run in the slides file's folder, environment plus `SLIDEV_PPTX_BROWSER=<Browser>` and `SLIDEV_PPTX_FFMPEG=<FFmpeg>`.
  - `func Run(ctx context.Context, cmd *exec.Cmd, out string, onEvent func(Event)) error`: starts it in a process group (Linux) or job object (Windows), as `internal/slidev` does; calls `onEvent` per stdout line; on `ctx` cancel stops the group (polite, then kill after 5 s) and returns `context.Canceled`; returns an error carrying the `error` event's message when the exit code isn't 0; always removes `out + ".partial"` unless the exit was clean.

- [ ] **Step 1: Write the failing tests** (the exporter is faked by the test binary re-run as a helper process, `TestHelperProcess`, which prints scripted lines and can sleep, fail or write `<out>.partial`):
  - `TestCommandArgsAndEnv`: editable has `--render`, no `--animations`; animated has both; both have `--progress json`; env has both variables; `Dir` is the slides file's folder.
  - `TestRunPassesEventsInOrder`: three scripted lines → three `Event`s with the right fields.
  - `TestRunCancelStopsTheExporterAndRemovesThePartialFile`: the helper writes `<out>.partial` and a child process, then sleeps; cancel → `context.Canceled`, no `.partial`, the child gone (its PID no longer runs).
  - `TestRunFailureCarriesTheErrorMessage`: last line `{"event":"error","message":"Slidev could not start"}`, exit 1 → error text contains it, no `.partial`.
- [ ] **Step 2: Run** `go test ./internal/export/` — Expected: FAIL (package missing).
- [ ] **Step 3: Implement** `run.go` and the two `proc_*.go`, sharing the stop sequence of `internal/slidev/proc_*.go` (copy the small helpers rather than export them from `slidev`).
- [ ] **Step 4: Run** `go test ./internal/export/ && GOOS=windows go vet ./internal/export/` — Expected: PASS.
- [ ] **Step 5: Commit** `Export: run slidev-pptx-export and read its progress`.

### Task B4: Progress text

**Files:**
- Create: `internal/export/progress.go`, `internal/export/progress_test.go`

**Interfaces:**
- Produces: `func NewTracker(file string, animations bool, now func() time.Time) *Tracker`; `func (t *Tracker) Apply(e Event)`; `func (t *Tracker) Head() string`; `func (t *Tracker) Step() (text string, fraction float64)`.
  - Head: `"<file>, editable"` or `"<file>, with animations"`.
  - Step texts: `"Unpacking"`, `"Starting Slidev"` (before `start`), `"Slide N of M"` (after `slide N`), `"Slide N of M, recording click K"` (after `record`, until the next `clip`, `state` or `slide`), `"Writing the file"` (after `write`); with `" · about X min left"` (or `"about 1 min left"`, `"less than a minute left"`) once `fraction ≥ 0.15` and 20 s have passed: time so far × (1 − fraction) / fraction, rounded up to whole minutes.
  - Fraction: (slides finished + states captured on the current slide ÷ its states) ÷ slides, at most 0.95 before `write`; 0.97 after `write`; 1 after `done`. It never goes down.

- [ ] **Step 1: Write the failing tests:** `TestTrackerSteps` replays a recorded event stream (11 slides; slide 4 has 3 states, click 2 recorded) with a fake clock, asserting `"Slide 4 of 11, recording click 2 · about 3 min left"` at the right point; `TestTrackerNoTimeLeftTooEarly`; `TestTrackerFractionNeverGoesDown`; `TestTrackerHead`.
- [ ] **Step 2: Run** `go test ./internal/export/ -run Tracker` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — Expected: PASS.
- [ ] **Step 5: Commit** `Export: the progress box's text and bar, from the exporter's events`.

### Task B5: The progress box and the finished box

**Files:**
- Create: `internal/ui/progress.go` (interface and headless), `internal/ui/progress_unix.go`, `internal/ui/progress_windows.go`, `internal/ui/progress_unix_test.go`, `internal/ui/progress_test.go`
- Modify: `build/mkwinres/main.go` only if the program's manifest lacks the Common Controls 6.0 dependency (check first)

**Interfaces:**
- Produces:
  - `type Progress interface{ Update(step string, fraction float64); Cancelled() <-chan struct{}; Close() }`
  - `func ShowProgress(title, head string) Progress` (platform)
  - `func Finished(title, msg string, buttons []string) int` (platform): index of the button pressed; −1 when closed.
  - `func NewHeadlessProgress(w io.Writer) Progress` and `func HeadlessFinished(w io.Writer, title, msg string, buttons []string) int` (JSON lines `{"event":"progress","step":…,"fraction":…}` and `{"event":"finished",…}`; returns −1).
- Linux: zenity `--progress --title=T --text=HEAD --percentage=0 --width=420` (no `--no-cancel`, no `--auto-close`), fed `# <step>\n` and `<percent>\n`; zenity exiting before `Close` means Cancel. kdialog: `--progressbar HEAD 100`, then `qdbus`/`gdbus` calls for `Set value`, `setLabelText` and `wasCancelled` on the service it prints. Neither: a notification at start, `Update` logs, `Cancelled` never fires. `Finished`: zenity `--info` with `--ok-label=<last button>` and `--extra-button` for the others (the label it prints is the answer); kdialog `--yesnocancel` with labels; neither: a notification and −1.
- Windows: `TaskDialogIndirect` from `comctl32.dll` with `TDF_SHOW_PROGRESS_BAR | TDF_CALLBACK_TIMER`, Cancel; the timer callback applies the latest `Update` with `TDM_SET_PROGRESS_BAR_POS` and `TDM_UPDATE_ELEMENT_TEXT`; `Close` sends `TDM_CLICK_BUTTON` with an ID the callback treats as done, not Cancel. `Finished` is a Task Dialog with one custom button per label.

- [ ] **Step 1: Write the failing tests:** `TestZenityProgressArgs`, `TestZenityProgressInput` (a fake zenity script records its stdin: `# Slide 4 of 11\n42\n`), `TestZenityExitMeansCancelled`, `TestZenityFinishedReturnsTheButton` (fake zenity prints `Show in folder`), `TestKdialogProgressCalls` (fake kdialog and qdbus record their arguments), `TestNoDialogProgramNotifiesAndNeverCancels`, `TestHeadlessProgressLines`. Use the existing seams in `dialogs_unix.go` (`lookPath`, the command starter) so fake programs come from a temp `PATH`.
- [ ] **Step 2: Run** `go test ./internal/ui/` — Expected: FAIL.
- [ ] **Step 3: Implement** Linux and headless; then Windows (compile-checked here).
- [ ] **Step 4: Run** `go test ./internal/ui/ && GOOS=windows go build ./... && GOOS=windows go vet ./internal/ui/` — Expected: PASS. Run `make winres` and check the manifest names `Microsoft.Windows.Common-Controls` version 6.
- [ ] **Step 5: Commit** `UI: a progress box with Cancel, and a finished box with buttons, on Linux and Windows`.

### Task B6: The export session

**Files:**
- Create: `internal/export/export.go`, `internal/export/export_test.go`
- Modify: `internal/app/app.go` (export `CodeQuestion`, used by both)

**Interfaces:**
- Consumes: `workspace.Extract`, `workspace.Remove`, `trust.Store`, `app.CodeQuestion`, `instance.Key`, Tasks B3 to B5.
- Produces:
  - `type Dialogs interface{ Confirm(title, msg string) bool; AskSavePath(suggest string) (string, bool); Error(title, msg string); ShowProgress(title, head string) ui.Progress; Finished(title, msg string, buttons []string) int }` with `func PlatformDialogs() Dialogs` and `func HeadlessDialogs(w io.Writer) Dialogs`.
  - `type Options struct{ Home, DeckPath string; Animations bool; Dialogs Dialogs; GOOS string; TrustCode, Replace bool; Run func(ctx context.Context, cmd *exec.Cmd, out string, onEvent func(Event)) error; Open func(path string) error; Reveal func(path string) error }` (`Run` defaults to Task B3's, `Open` to `browser.Open`, `Reveal` to `xdg-open <folder>` or `explorer /select,<path>`).
  - `func Export(ctx context.Context, o Options) (Result, error)`; `type Result struct{ Path string; Slides, Clips int; Bytes int64; Notes []string }`.
  - `var ErrBusy`, `var ErrNotTrusted` (wrapping `app.ErrNotTrusted`'s meaning), `ErrCancelled`.
- Behaviour, in order: lock `<home>/instances/<instance.Key(deck)>.export` (created exclusively, holding the PID; a lock whose PID no longer runs is taken over) else `ErrBusy` with "Already exporting <file>"; choose the output (Global Constraints); if it exists and not `Replace`, `Confirm("Replace <name>?")`, No → nothing run; if its folder can't be written, `AskSavePath(<home dir>/<name>)`, cancelled → nothing run; extract; code question as opening asks it (trusted store, remember Yes); progress box; run; on Cancel → `ErrCancelled`; on failure → `Error` with one sentence (`"Couldn't export <file>: <first line of the message>. The log is in <home>/logs."`); on success → `Finished("Exported <name>", "<slides> slides, <clips> with a video, <MB> MB" + notes, ["Open", "Show in folder", "Close"])`. Notes: up to three `notice` texts that are neither `… clip, … MB.` nor `… exported as a picture …`; picture notices become one line, `"<n> parts are pictures"`. The work folder and the lock are removed in every case.

- [ ] **Step 1: Write the failing tests** (a fake `Run` replays scripted events and can block, fail or write `<out>.partial`; `HeadlessDialogs` with scripted answers):
  - `TestExportWritesNextToTheFile` (editable `talk.pptx`, animated `talk-animations.pptx`; the finished box text and buttons)
  - `TestExportAsksBeforeReplacing` (No → `Run` never called, old file untouched)
  - `TestExportAsksWhereWhenTheFolderCantBeWritten` (a read-only temp folder)
  - `TestExportAsksTheCodeQuestionOnce` (No → `ErrNotTrusted`, nothing run, work folder gone; Yes is remembered)
  - `TestExportCancelLeavesNothing` (cancel while `Run` blocks → no `.pptx`, no `.partial`, no work folder, no lock)
  - `TestExportFailureIsOneSentence` (message and log folder named; no file)
  - `TestExportSameFileTwiceIsRefused` and `TestExportDifferentFilesTogether`
  - `TestExportTakesOverALockFromAKilledExport` (a lock file with a PID that doesn't run)
  - `TestExportNotes` (clip and picture notices summarised as above)
  - `TestExportPathWithSpacesAndQuotes` (`a "b" $c ø.sptx` → `a "b" $c ø.pptx`)
- [ ] **Step 2: Run** `go test ./internal/export/` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./... && GOOS=windows go vet ./...` — Expected: PASS, opening's tests unchanged.
- [ ] **Step 5: Commit** `Export: unpack, ask, run and report an export of a .sptx`.

### Task B7: `stagepack export-pptx`

**Files:**
- Modify: `cmd/stagepack/main.go`, `cmd/stagepack/main_test.go`

**Interfaces:**
- Produces: `stagepack export-pptx [--animations] [--home DIR] [--headless] [--trust-code] [--yes] FILE.sptx`; `usage` lists `export-pptx`. `--headless` uses `HeadlessDialogs(stdout)`; `--yes` sets `Replace`. Exit codes: 0 done, 1 failed, 2 usage, 3 cancelled or not trusted, 4 busy. `--home` defaults as for `open`.

- [ ] **Step 1: Write the failing tests:** dispatch to an injected `exportDeck` (as `openDeck` is) with the right `export.Options`; usage error without a file or with two; each error maps to its exit code.
- [ ] **Step 2: Run** `go test ./cmd/stagepack/` — Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `stagepack export-pptx: export a .sptx to PowerPoint`.

### Task B8: The right-click entries

**Files:**
- Modify: `internal/install/install.go` (`RegistryPlan`), `internal/install/xdg.go` (`xdgFile` gains `mode fs.FileMode`, `xdgFiles`, `ownedXDGFiles`), `internal/install/install_test.go`, `internal/install/xdg_test.go`

**Interfaces:**
- Produces:
  - Registry (HKCU, under the existing ProgID): `shell` default `open` (so double-click still opens); `shell\exportpptx` `MUIVerb` = `Export to PowerPoint (editable)`, `shell\exportpptx\command` = `"<exe>" export-pptx "%1"`; `shell\exportpptxanim` `MUIVerb` = `Export to PowerPoint (with animations)`, `shell\exportpptxanim\command` = `"<exe>" export-pptx --animations "%1"`. Uninstall already removes the ProgID tree: check it does so recursively.
  - XDG (under the data home, written and removed with the others): `kio/servicemenus/<AppID>-export.desktop` (mode 0755; `Type=Service`, `MimeType=application/x-stagepack;`, `X-KDE-ServiceTypes=KonqPopupMenu/Plugin`, `Actions=editable;animations;`, one `[Desktop Action …]` each with `Name=` the wording and `Exec=` `desktopExecArg(exe)` `export-pptx [--animations] %f`); `nautilus/scripts/Export to PowerPoint (editable)` and `nautilus/scripts/Export to PowerPoint (with animations)` (mode 0755; `#!/bin/sh` then `for f in "$@"; do <quoted exe> export-pptx [--animations] "$f"; done`); `applications/<AppID>-export.desktop` and `applications/<AppID>-export-animations.desktop` (`NoDisplay=true`, the MIME type, `Name=` the wording, `Exec=… export-pptx [--animations] %f`).

- [ ] **Step 1: Write the failing tests:** `TestRegistryPlan` gains the five values; `TestRegistryCommandQuotesPath` covers both verbs; `TestXDGFilesIncludeTheExportEntries` (names, `Exec` lines, modes); `TestNautilusScriptPassesPathsIntact` runs the generated script with `sh` against a fake exe that records its arguments, for `a "b" $c ø.sptx` and two files at once; `TestUninstallRemovesTheExportEntries`.
- [ ] **Step 2: Run** `go test ./internal/install/` — Expected: FAIL.
- [ ] **Step 3: Implement.** The Nautilus script quotes the exe for `sh` (single quotes, `'\''` for a quote in the path).
- [ ] **Step 4: Run** `go test ./... && GOOS=windows go vet ./internal/install/` — Expected: PASS.
- [ ] **Step 5: Commit** `Install: Export to PowerPoint in the right-click menu on Windows, KDE and GNOME`.

### Task B9: READMEs

**Files:**
- Modify: `README.md`, `addon/README.md`, `npm/README.md`

- [ ] **Step 1:** README "For presenters": the two entries and where each desktop shows them (Windows 11's "Show more options"; GNOME's Scripts and Open With; KDE's plain entries), the progress box, the output names, Cancel, and that the animated copy is for PowerPoint for the web and desktop PowerPoint (after Task B11, say what was checked); "For authors": `stagepack export-pptx`; "Install": the one-line installs bring the export too, with the sizes from Task B1; Layout: the new files. `addon/README.md` and `npm/README.md`: one line each pointing to it.
- [ ] **Step 2:** Commit `README: Export to PowerPoint from the right-click menu`.

### Task B10: Linux end to end

- [ ] **Step 1:** `make build-linux`, then install into a temporary home: `dist/linux/stagepack install --yes --home <tmp>` with `XDG_DATA_HOME=<tmp-data>`.
- [ ] **Step 2:** Pack `examples/<an example presentation>` and the exporter's fixture deck (`test/fixtures/animated-deck` in the exporter repo, without its `node_modules`) with `stagepack pack`; run `<tmp>/stagepack export-pptx --headless --trust-code` and `--animations` on each.
  Expected: four `.pptx` files next to the `.sptx` files, no `.partial`, no work folders left under `<tmp>/runtimes/53/work`; the exporter found the runtime's Slidev by walking up from the work folder, with no option for it (the design asks this to be confirmed: if it didn't, add a `render.slidevCli` option in Part A, test first, and pass it in Task B3's `Command`); the animated fixture export has 4 slides and one `.mp4`; each passes the exporter's `lintPackage` (run `node -e` with `import("<exporter>/test/lint.mjs")`).
- [ ] **Step 3:** Check `<tmp-data>/nautilus/scripts/` and `kio/servicemenus/` hold the entries, executable. Ask the user to right-click a `.sptx` in GNOME Files with the real install (`make build-linux` then `stagepack install`), choose each entry, and watch the box; record what they report.
- [ ] **Step 4:** Commit any fixes, each with its test first.

### Task B11: Desktop PowerPoint (needs the user)

- [ ] **Step 1:** Export the test presentation with `--animations` (the same deck the PowerPoint for the web check used) and hand it to the user with a checklist: open it in desktop PowerPoint on Windows, run the slide show from slide 1 with the right arrow only, and for each key press say whether it matches Slidev's next state; note any video that doesn't start by itself, any native shape missing, and what Left does.
- [ ] **Step 2:** Each problem: a failing test in the exporter (Part A's branch) that pins the fix, the fix, `npm test && npm run test:render`, then the web check again on the user's upload.
- [ ] **Step 3:** Record the results in the exporter's design doc and README ("Checked on"), and in Stagepack's README (Task B9).

### Task B12: Pin the published exporter and open the PRs

- [ ] **Step 1 (the user):** merge slidev-pptx-export PRs #3, #2, #4 and Part A's PR, and publish a new version to npm.
- [ ] **Step 2:** `runtime/package.json`: `"slidev-pptx-export": "<published version>"`; remove `runtime/vendor/`; `npm install --package-lock-only`; `make build-linux build-windows`.
- [ ] **Step 3:** Run `go test ./... && make licenses-go` and repeat Task B10 Step 2. Expected: PASS, same results.
- [ ] **Step 4:** Commit `Runtime: slidev-pptx-export <version>`; push `claude/export-pptx`; open the Stagepack PR. The body says what was checked (Linux end to end, GNOME right-click as the user reported it, desktop PowerPoint as the user reported it) and what wasn't (Stagepack on a real Windows machine, if still not done).
