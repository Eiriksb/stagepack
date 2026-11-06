# Export to PowerPoint from a .sptx

Status: design, awaiting approval. Date: 2026-11-06.

## Intent

Someone has a `.sptx` and wants a PowerPoint version of it without opening a terminal. They right-click the file,
choose an export, watch a progress box, and get a `.pptx` next to the `.sptx`. That works on every platform
Stagepack supports, for people who only installed Stagepack: no Node, no npm, nothing else to set up.

The export itself is [slidev-pptx-export](https://github.com/Eiriksb/slidev-pptx-export), which runs a Slidev
deck in a browser and converts what it draws. Stagepack already carries Node and a pinned Slidev. This design adds
the exporter, a headless browser for it and ffmpeg to what Stagepack ships, plus the menu, the command behind it
and the progress box.

## Who decided what

- **Decided by the user:** a right-click entry on `.sptx` files; on every platform Stagepack supports; for anyone
  who wants to turn a `.sptx` into a `.pptx` quickly; a choice of both exports (editable, and with animations); a
  progress box in the style of Stagepack's "Opening…" box, with the details of a step list folded into its text;
  no Node dependency, so the exporter ships inside Stagepack; ffmpeg bundled, so every platform gets videos for
  clicks that move. After a reviewer's questions: a headless browser bundled on both platforms (most Linux
  desktops have no browser Playwright can drive, and a bundled one sidesteps company policies on Edge and Chrome);
  a GPL ffmpeg shipped as a separate program, with Stagepack's licence policy reworded to allow that; and the
  animated export checked, and fixed where needed, in desktop PowerPoint as well as PowerPoint for the web.
- **The author's calls, open to correction:** everything below that the user did not say, including the file
  names, the menu wording, the progress protocol and the platform mechanics.

## Goals

- Right-click a `.sptx` on Windows x64, Linux x86_64 with GNOME, or Linux with KDE, and choose
  **Export to PowerPoint (editable)** or **Export to PowerPoint (with animations)**.
- A progress box shows what is happening and roughly how long is left, and can be cancelled.
- The result is `<name>.pptx` or `<name>-animations.pptx` next to the `.sptx`, with **Open** and **Show in folder**.
- Nothing to install beyond Stagepack, and no dependence on which browser the computer has. The export works
  offline when the presentation does (map tiles from the internet need the network, as they do when presenting).
- The animated export plays correctly in desktop PowerPoint on Windows as well as in PowerPoint for the web.

## Non-goals

- macOS, which Stagepack doesn't support.
- Choosing export options in the menu (slide size, fonts, `--videos`). The command takes the exporter's config
  file the deck carries, if any, as the CLI does.
- Exporting a Slidev folder that isn't packed. The addon or the exporter's own CLI covers that.

## Anti-goals

- A Node, npm or Playwright install step for the person exporting.
- A download at export time.
- A `.pptx` left half-written after Cancel or a failure.
- Running a presentation's code without the question Stagepack asks before opening one.

## Behaviour

### The menu

| Desktop | Where the entries appear | How Stagepack's installer adds them |
|---|---|---|
| Windows (Explorer) | Right-click; on Windows 11 under "Show more options" | Two verbs under the existing ProgID in HKCU: `shell\exportpptx` and `shell\exportpptxanim`, each with `MUIVerb` and `command` (`"<exe>" export-pptx "%1"`, with `--animations` for the second). `RegistryPlan` in `internal/install/install.go` gains them. |
| KDE (Dolphin) | Right-click, as plain entries | A service menu, `~/.local/share/kio/servicemenus/<AppID>-export.desktop`, `MimeType=application/x-stagepack`, with two actions. |
| GNOME (Files) | Right-click → Scripts, and Open With | Two executable scripts in `~/.local/share/nautilus/scripts/` named after the entries, each running `stagepack export-pptx` on the selected file. Two `NoDisplay=true` desktop entries with the MIME type, so they appear under Open With. A plain top-level entry would need a Nautilus extension, which needs `python3-nautilus`: not used. |

`stagepack install` adds them, `stagepack uninstall` removes exactly them, as it does the rest. The wording is
"Export to PowerPoint (editable)" and "Export to PowerPoint (with animations)".

### The command

`stagepack export-pptx [--animations] FILE.sptx`, also usable from a terminal. It:

1. Takes a lock for that file, so a second export of the same file at the same time is refused with a message.
   Exports of different files may run together.
2. Unpacks the presentation with `workspace.Extract`, as opening does, under `runtimes/<major>/work/`.
3. Asks the "contains code" question if the presentation has code and isn't trusted yet, with the same text,
   trust store and remembered answer as opening. A No ends the export with nothing written.
4. Runs the exporter with Stagepack's Node, the runtime's pinned Slidev, the bundled headless browser and the
   bundled ffmpeg; render mode for the editable export, `--animations` for the other.
5. Writes `<name>.pptx` or `<name>-animations.pptx` next to the `.sptx`. If that file exists, it asks before
   replacing it. If the folder can't be written, it asks where to save, using the existing save dialog.
6. Removes the work folder, as closing a presentation does.

### The progress box

The box looks like Stagepack's "Opening…" box: a title, one or two lines of text, a bar, and **Cancel**.

- Text: the file and kind, then the current step and time left, for example
  "talk.sptx, with animations" and "Slide 4 of 11, recording click 2 · about 3 min left". Steps are
  "Unpacking", "Starting Slidev", "Slide N of M" (with ", recording click K" while a click is recorded),
  "Writing the file".
- The bar follows the click states captured out of the total, plus the recorded clips, as the exporter reports
  them. Time left is estimated from the time so far, and shown only once there is enough to go on.
- **Cancel** stops the exporter and its Slidev and browser, removes the work folder, and leaves only what was
  there before.
- When it's done, the box becomes a finished box: "Exported talk-animations.pptx", "23 slides, 11 with a video,
  29.9 MB", any notices worth reading (a click that ends differently from a fresh load, a part exported as a
  picture count), and **Open**, **Show in folder** and **Close**.
- On failure: one sentence saying what went wrong and what to do, and **Close**. The full log goes to Stagepack's
  log folder, as for opening.

Platform mechanics:

- **Linux:** `zenity --progress` with a percentage and `# text` lines on its input, and Cancel enabled; the finished
  box is `zenity --info` with extra buttons. `kdialog --progressbar` on KDE, driven over D-Bus. Without either,
  a notification at the start and at the end, as Stagepack's dialogs already fall back.
- **Windows:** today's `Busy` is a message box with only OK, so this is new: a Task Dialog
  (`TaskDialogIndirect`, comctl32 v6) with a progress bar, text updated from its callback, and Cancel; its
  finished state has command buttons. It needs the common-controls v6 manifest in the program's resources
  (`build/mkwinres`). No new Go module: `golang.org/x/sys/windows` is enough.

## What changes where

### slidev-pptx-export

Small additions, each useful outside Stagepack too:

- **The browser:** use `playwright-core` when neither `playwright-chromium` nor `playwright` is there, with an
  executable path given by a new `render.browser` option (and a matching environment variable). Stagepack points it
  at its bundled headless browser.
- **ffmpeg:** a new `render.ffmpeg` option (and environment variable) naming the ffmpeg program to use, for every
  use of ffmpeg: recording clips (`findEncoder`, tried before the PATH search) and also the editable export's
  animated pictures of a deck's own videos and its frame-rate reads (`src/render/animate.mjs`, which today calls
  `ffmpeg` from PATH).
- **Progress:** `--progress json` writes one JSON line per event to stdout: the deck's shape (slides, click states,
  so a total is known early), each state captured, each click recorded or encoded, each notice, and the end
  (path, slides, clips, bytes) or the error. Human-readable notices still go to stderr.
- **No half-written file:** the `.pptx` is written to a temporary name beside the target and renamed at the end.
- Slidev is found as now, by walking up from the deck: a Stagepack work folder sits under
  `runtimes/<major>/`, whose `node_modules/@slidev/cli` the walk reaches. To be confirmed by the builder.

These land as one PR on top of PR #4 (the animations), and need a published release before Stagepack can pin it.

### Stagepack

- `runtime/package.json`: `slidev-pptx-export` and `playwright-core`, pinned, through the existing `npm ci` and
  `build/licenses.mjs` (both are Apache-2.0 or MIT; their dependencies must pass the same list).
- `build/versions.env` and `build/fetch.sh`: Playwright's `chromium-headless-shell` build for Linux x64 and
  Windows x64, at the revision the pinned `playwright-core` expects, with checksums, as for Node. It lands in
  `dist/<os>/browser-shell/`, then in the payload and the install folder. It is mirrored in Stagepack's own release
  assets, as ffmpeg is below, since Playwright's download host keeps old builds only for a while. Expect about
  100 MB more per download; measure and state it in the README. Its third-party licences (Chromium's, including
  its LGPL ffmpeg) go into the licence file under the reworded policy.
- `build/versions.env` and `build/fetch.sh`: a pinned ffmpeg for Linux x64 and Windows x64, with checksums, as
  for Node. It lands in `dist/<os>/ffmpeg/`, then in the payload and the install folder as `ffmpeg/ffmpeg[.exe]`.
  ffmpeg.org publishes no binaries and third-party builds are pruned, so Stagepack's own release assets carry the
  pinned binaries and, for a GPL build, the exact source of ffmpeg and every library linked into it. The author's
  call: mirror a pinned third-party static build rather than build ffmpeg here, which would add a Windows
  cross-compile; a minimal build of our own (image2, mjpeg, gif, x264, mp4) is the fallback if size matters.
- `cmd/stagepack`: the `export-pptx` subcommand.
- `internal/app` or a new `internal/export`: the export session (lock, unpack, trust, run, progress, finish),
  sharing what opening already has.
- `internal/ui`: the progress box and the finished box on both platforms, plus a headless form for tests and
  scripts, as `ui.NewHeadless` does for opening.
- `internal/install`: the registry verbs, the KDE service menu, the Nautilus scripts and the Open With entries,
  added and removed with the rest.
- README: the right-click export under "For presenters" and the layout list; the Licences section, reworded (below);
  a line in "Install" saying the one-line installs now bring the export too, and the larger download. The addon
  README points to it.

### One-line installs

Stagepack's own installers (`Stagepack-Setup.exe`, the `curl … | sh` line, `npx stagepack install`) bring
everything the export needs. slidev-pptx-export's README gains a short "From a .sptx" section naming them, and
keeps its npm instructions for the CLI.

## Release order

1. The exporter PR above, on top of PR #4, including any fixes the desktop PowerPoint pass needs.
2. Merge #3, #2, #4 and that PR; publish a new slidev-pptx-export version. Publishing is the user's step.
3. Stagepack pins that version in its runtime, adds ffmpeg, the command, the boxes and the menu.

## Licences

Stagepack's README says it "ships only permissively licensed components, plus the weak-copyleft exceptions", and
that zenity and kdialog, being GPL, are "not linked or shipped". An ffmpeg that writes H.264 with libx264 is GPL,
and the headless browser carries Chromium's own third-party code, including an LGPL ffmpeg.

Decided by the user: ship the GPL ffmpeg as a separate program and reword the policy. The new wording: the
viewer and everything it links stay permissive (with the existing weak-copyleft exceptions); separate programs it
runs as their own processes (Node, the headless browser, ffmpeg) may be under other open-source licences,
including GPL and LGPL, shipped unmodified, each with its licence text in the third-party licence file and its exact
source in Stagepack's release assets. `build/licenses.mjs` keeps checking the npm side as now; a new check lists
the bundled programs, their versions, licences and source archives, and fails when one is missing.

## Verification

- Go unit tests: the registry plan with the new verbs, the service menu, scripts and desktop entries, their
  removal, the export session against a fake exporter (lock, trust, replace question, cancel leaving nothing,
  failure text), and the progress text from a recorded event stream.
- Exporter tests: the new options, the progress events, and the temporary file on failure and on success.
- End to end on Linux: install a local build, right-click a `.sptx` in GNOME Files, run both exports of a fixture
  presentation, and check the files lint clean, as slidev-pptx-export's tests do.
- Desktop PowerPoint: the animated export of the test presentation is run as a slide show in desktop PowerPoint on
  Windows, with the keyboard, against Slidev's click states, as it was on PowerPoint for the web. What fails is
  fixed in the exporter, with a test, and checked on the web again. This needs a Windows machine with PowerPoint,
  run by the user or lent for it; it can't be checked from here.
- Windows: cross-compiled and unit tested here. Running Stagepack itself on a real Windows machine is already one of
  its open items; the desktop PowerPoint pass above is a good moment to do both.

## Left to the builder

The JSON event names and fields, the time-left estimate, the lock's form, how kdialog's bar is driven, the Task
Dialog's exact layout, the size cutoff for listing notices in the finished box, and whether the export session lives
in `internal/app` or a package of its own.
