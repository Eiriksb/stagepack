# Stagepack

Stagepack packs a [Slidev](https://sli.dev) talk into one file, `talk.sptx`, that opens with a double-click.
The viewer brings its own Node and a pinned Slidev, so the presenter needs nothing installed: no Node, no npm.
It works offline when the talk bundles its fonts (see "Fonts" below). The talk opens in Slidev's presenter
view, and edits to the speaker notes are saved back into the `.sptx` file.

A talk can carry its own code (a Vite config, `setup/` files, Vue components, bundled packages), and Slidev
runs that code as you, like opening any Slidev project. So before it shows such a talk, the viewer asks, once
for each version of the talk, like Office does for macros. Talks of only slides, styles, pictures and fonts
open without asking.

Status: the packer, the viewer and the per-user install (file type, tray icon, dialogs) work on Linux
(tested on Ubuntu with GNOME). The Windows installer is built and tested as far as cross-compiling and unit
tests go, but see "Open items" below.

## For presenters

You get two files: `Stagepack-Setup.exe` (the same one for every talk) and the talk, a `.sptx` file. The
same text is in `PRESENTER.txt`, which `make build-windows` puts next to the installer.

1. **Download both files first.** Don't open the talk from the email or from the Teams preview: save both
   files to a folder, then work from there.
2. **Run `Stagepack-Setup.exe` once.** Double-click it and answer Yes; it needs no administrator rights. On
   "Windows protected your PC", click **More info**, then **Run anyway**. The installer is not signed with
   a purchased certificate, so Windows doesn't know it yet.
3. **Double-click the `.sptx` file.** The first start can take a moment; "Opening …" is shown meanwhile.
   The presenter view then opens in your browser, with the speaker notes. To show the slides to the
   audience, click the screen icon in the presenter view, or use the Stagepack tray icon (bottom right, next
   to the clock; it may be under the small arrow) and choose **Open audience view**. Drag that window to the
   projector.
4. **Notes are saved into the file.** Edit the speaker notes as you like; they are written into the `.sptx`
   file. Hover over or click the tray icon to see "Notes saved".
5. **To finish, use the tray icon and choose Stop.** The notes are saved one last time and the talk closes.

**Save a copy…** in the tray menu saves the talk, with your notes, under another name; notes you edit after
that go into the copy. Use it when the tray says **Notes not saved in the file**: the `.sptx` file could not
be written (it is read-only, or the USB stick was removed). Your notes are kept meanwhile, and Save a copy
puts them in a new file. **Temporary copy** means the talk was opened from an email or a preview, so notes
would be lost with it: Save a copy keeps them.

If you open the same file twice, the second launch brings up the running talk instead of starting another.
The viewer talks only to `127.0.0.1`. The file is replaced atomically when notes are saved, so a crash leaves
the old or the new file, never a broken one.

**"… contains code"**: some talks come with code of their own (for example an interactive component), which
runs on your computer, with access to your files, while the talk is shown. Before it starts such a talk,
Stagepack lists what the code is and asks. Choose **Yes** only if you trust the person who sent you the file;
it then remembers your answer for this version of the talk, so you aren't asked again when you reopen it
(notes you edit don't count as a change). **No** closes it without running anything. If the talk is changed
later, you are asked again. Talks without code never ask.

To remove Stagepack: **Settings → Apps → Stagepack → Uninstall**. Your `.sptx` files are not touched.

### On Linux

There is no setup program: the Linux build (`make build-linux`, the folder `dist/linux` with `stagepack`,
`node/` and `runtimes/` in it) installs itself for the current user, without sudo.

1. **Install once:** run `dist/linux/stagepack install` in a terminal (`--yes` skips the question). It copies
   the viewer, Node and the Slidev runtime to `~/.local/share/stagepack` (`$XDG_DATA_HOME/stagepack`) and
   registers `.sptx` files as their own type, `application/x-stagepack`, opened by Stagepack, so the file
   manager no longer treats them as zip files.
2. **Double-click the `.sptx` file**, as on Windows. "Opening …" is shown meanwhile, then the presenter view
   opens in your default browser.
3. **The tray icon** (top right on Ubuntu) has the same menu as on Windows: the notes status, **Open
   presenter**, **Open audience view**, **Save a copy…** and **Stop**. Ubuntu shows tray icons out of the box;
   on other GNOME desktops turn on the AppIndicator extension. Where no tray is shown, a small window with the
   same buttons stands in for it; closing that window stops the talk.
4. **Dialogs** (errors, "Save a copy…", and the "… contains code" question) are shown with `zenity`, or
   `kdialog` on KDE. Without either, messages go to the log and to a desktop notification, and the code
   question is answered No: such a talk does not open until zenity is installed.
5. **To remove it:** `~/.local/share/stagepack/stagepack uninstall`. It removes exactly what the install
   added (the folder, the MIME type, the desktop entry, the icons and the default-application entry) and, as on
   Windows, keeps notes that are not yet in any talk. Your `.sptx` files are not touched.

## For authors

Make a normal Slidev project (a folder with `slides.md`), then:

```
stagepack pack DIR -o talk.sptx
```

Options:

| flag | meaning |
|---|---|
| `-o OUT` | the `.sptx` file to write |
| `--entry slides.md` | the slides file, relative to `DIR` |
| `--title T` | talk title (default: the headmatter title, else the folder name) |
| `--exclude PREFIX` | leave out a path prefix (whole path components), repeatable |
| `--meta k=v` | free-form manifest metadata, repeatable |

Packing leaves out of the file whatever the pinned runtime already provides (Slidev itself, its default theme,
the Inter and JetBrains Mono fonts), so a talk without extra dependencies is just your slides and media. Other
`node_modules` your talk needs are included. If the talk has no local favicon, a bundled one
(`public/stagepack-favicon.svg`) is added so the viewer makes no network request. Your folder is never modified.

`stagepack open talk.sptx` does what a double-click does.

### Code in a talk

The viewer asks the presenter before it shows a talk that contains code (see "For presenters"), and
`stagepack pack` prints a note naming each thing that makes it so. These count:

- files named `vite.config*`, `uno.config*`, `unocss.config*`, `postcss.config*` or `.postcssrc*`;
- anything under `setup/`;
- any `.vue`, `.js`, `.mjs`, `.cjs`, `.ts`, `.mts`, `.cts`, `.jsx`, `.tsx`, `.node` or `.wasm` file, also in
  `public/`;
- a bundled package that holds such a file, or whose `package.json` lists Slidev addons (packages of only CSS
  and fonts, like Fontsource, don't count);
- any theme but the one the runtime bundles, and any addons (the runtime bundles none);
- `"slidev": {"addons": [...]}` or a `"postcss"` key in any `package.json`, or a `package.json` that isn't
  valid JSON.

Slidev reads the theme and addons from the headmatter with a full YAML parser, and YAML has many ways to write
a key or carry a value over lines. So the viewer doesn't guess: before it asks, it runs Slidev's own parser
(the runtime's `@slidev/parser`, with the bundled Node, on the unpacked slides file only) and judges what that
returns. Nothing from the talk runs for this. The only theme that needs no question is the bundled one
(`default`, `none` or `@slidev/theme-default`). Any other theme and any addons count, also those set under
`config:`, and so does a headmatter that can't be read. A changed theme or addon list asks again.

`stagepack pack` has no Node of its own, so its note uses a rougher check: it allows only `theme: default` on its
own line in the `---` block at the top of the slides file, and names any other `theme:` or `addons:` line, in
code blocks and slide text too. The viewer may not ask about all of those. File names are compared ignoring
case, as Windows finds them.

The answer is remembered per talk (its id) and version: a hash of every file in it except the slides file, which
note edits change, and of the reasons it contains code (so of its theme and addons). Any other change,
repacking included, asks again. The list is `trusted.json` in the
viewer's home. Where nobody can be asked (`stagepack open --headless`, and on Linux without `zenity` or
`kdialog`), the answer is No; `stagepack open --trust-code FILE` runs a talk you trust without asking, and does not add it
to the list.

### Fonts

Slidev's default font provider is Google Fonts: the browser loads the talk's fonts from the internet when it
is shown, so without a network the slides fall back to other fonts. To make a talk work offline, bundle its
fonts:

1. Install a [Fontsource](https://fontsource.org) package per font, for example
   `npm i @fontsource-variable/roboto`, and import it in the talk's `style.css`:
   `@import '@fontsource-variable/roboto';`. Inter and JetBrains Mono (`@fontsource-variable/inter`,
   `@fontsource-variable/jetbrains-mono`) are in the viewer's runtime already: import them, no install needed.
2. Set the provider to `none` in the headmatter of `slides.md`:

   ```yaml
   fonts:
     sans: Roboto Variable
     mono: JetBrains Mono Variable
     provider: none
   ```

`stagepack pack` warns when the headmatter does not set `provider: none`; it does not change the talk.

## Building from source

Needs a Linux (amd64) build host with `curl`, `tar`, `unzip` and `npm`. Go and Node are downloaded once into
`.cache/`, pinned and checksummed (`build/versions.env`, `build/fetch.sh`).

| target | what it does |
|---|---|
| `make test` | gofmt check, `go vet`, unit tests |
| `make e2e` | end-to-end tests against the Linux build in `dist/linux` (skipped when it is missing) |
| `make node OS=linux` | puts Node in `dist/linux/node/` (`OS=win32` for Windows) |
| `make runtime OS=linux` | installs the pinned Slidev runtime in `dist/linux/runtimes/53/` and checks its licences (online) |
| `make build OS=linux` | builds `dist/linux/stagepack` (`OS=win32` cross-compiles `stagepack.exe`) |
| `make build-linux` | `dist/linux/stagepack` with `node/` and `runtimes/` beside it; fetches Node and installs the runtime only if they are missing |
| `make build-windows` | `dist/windows/Stagepack-Setup.exe` and `PRESENTER.txt` beside it; the same for Windows (Node, runtime, viewer, payload, installer) |
| `make licenses-go` | the licences of the Go modules the Linux and Windows programs link (`build/go-licenses.tsv`); fails on a module that isn't permissive or isn't in the table |
| `make setup` | builds `dist/windows/Stagepack-Setup.exe`: the installer, with the Node and Slidev runtime and the viewer inside (needs `make node runtime OS=win32`; fetches go-winres, 0BSD, once for the icon) |

For a Windows check without Windows: `GOOS=windows go vet ./...` and `GOOS=windows go build ./cmd/stagepack`.

To use the Linux build on this machine: `make build-linux`, then `dist/linux/stagepack install` (see "On Linux"
above). Run it again after a new build to update the installed copy; a talk must not be open meanwhile.

## Layout

The viewer's home (on Windows `%LOCALAPPDATA%\Stagepack\`, on Linux `~/.local/share/stagepack/`) holds:

```
stagepack.exe                                     the viewer (Windows; on Linux the stagepack program)
.stagepack-install                                install marker: uninstall deletes only a folder with it
node/node[.exe]                                   Node 24 (build/versions.env)
runtimes/53/package.json, package-lock.json       the pinned runtime (runtime/)
runtimes/53/pnpm-workspace.yaml                   marks runtimes/53/ as Vite's workspace root
runtimes/53/node_modules/@slidev/cli/bin/slidev.mjs
runtimes/53/work/<id>/                            one opened talk, unpacked
journal/<key>.json                                note edits not yet in the .sptx file (kept by uninstall)
trusted.json                                      talks allowed to run their code: id and hash of that version
logs/viewer.log                                   the viewer's log (viewer.log.1: the previous one)
logs/<key>.log                                    Slidev's output for one talk
instances/<hash>.json, .lock                      one running talk: process, port, URL
```

On Linux the install also writes, below `~/.local/share` (`$XDG_DATA_HOME`), and nothing else:

```
mime/packages/stagepack.xml                       application/x-stagepack: *.sptx, a subclass of application/zip
applications/stagepack.desktop                    Exec=<home>/stagepack open %f, hidden from the app menu
icons/hicolor/{256x256,48x48,32x32,16x16}/apps/stagepack.png   the icon (from assets/stagepack.ico)
```

and runs `update-mime-database`, `update-desktop-database` and `xdg-mime default stagepack.desktop
application/x-stagepack` (which writes `~/.config/mimeapps.list`). The names come from `internal/brand`.

A `.sptx` file is a zip. At its root is `stagepack.json`, the manifest (format version, id, title, created and
updated times, Slidev version, entry file, metadata), next to the talk's own files (`slides.md`, `public/`, ...).

A talk is unpacked to `runtimes/53/work/<id>/` and Slidev is started there
(`node runtimes/53/node_modules/@slidev/cli/bin/slidev.mjs ./slides.md --port N --open false`). Slidev, the
theme and the fonts resolve by Node's normal parent lookup to `runtimes/53/node_modules`; no symlink, junction
or generated `vite.config.ts` is needed. Because `runtimes/53/` has `pnpm-workspace.yaml`, Vite's
`server.fs.allow` covers the whole of `runtimes/53/`, so the font files are served (200, not 403). A
`node_modules` symlink in the talk folder pointing outside it does **not** work: Vite answers the font woff2
files with 403 because they are outside `server.fs.allow`.

Slidev writes its generated files to `work/<id>/node_modules/.slidev/`, so the work folder must be writable
and gets a `node_modules/` even when the talk vendors nothing.

The Go code is in `cmd/stagepack` (command line) and `internal/` (`brand` holds the product's names; `pack`,
`deck`, `workspace`, `trust`, `save`, `slidev`, `instance`, `ui`, `browser`, `app`).

## Measured

On Linux, a recent laptop, warm disk cache, a talk with a few hundred MB of video:

| what | time |
|---|---|
| Slidev start to first `200` on `/presenter/` | 0.74-0.86 s (3 runs) |
| `/presenter/` opened to network idle (headless Chrome) | 2.2-2.3 s |
| `npm ci` of the runtime (from npm's cache) | 3 s |

Every slide loads with no 4xx/5xx, the `@fontsource-variable` woff2 files load with 200, and the cover renders
in Inter Variable.

| size | Linux | Windows |
|---|---|---|
| `runtimes/53/node_modules` (685 packages) | 596 MB | 574 MB (27 393 files) |
| `node/` | 208 MB | 110 MB |

## Licences

Stagepack is MIT licensed (see `LICENSE`). It ships only permissively licensed components, plus the
weak-copyleft exceptions below, unmodified.

`build/runtime.sh` runs `build/licenses.mjs`, which writes `runtimes/53/THIRD_PARTY_LICENSES.txt` and fails on
any package outside the permissive list (MIT, ISC, BSD-2/3-Clause, Apache-2.0, 0BSD, OFL-1.1, Unlicense, CC0-1.0,
BlueOak-1.0.0, plus PSF-2.0, Python-2.0, Zlib and CC-BY-4.0). Two packages with no licence field, khroma 2.1.0
and zigpty 0.2.1, are MIT by their licence files; they are pinned by exact version, so a new version fails until
it is checked again. Three are allowed by exception, by exact version: lightningcss 1.33.0 with its platform
binding (MPL-2.0) and elkjs 0.9.3 (EPL-2.0). They are weak, file-level copyleft. Slidev 53 can't do without
them: Vite 8 depends on lightningcss and mermaid on elkjs. They ship unmodified as published on npm and run in
Slidev's own Node process. The build prints them as a warning, and `THIRD_PARTY_LICENSES.txt` lists them in a
separate "Weak-copyleft components (unmodified)" section, with their licence texts and their source: the npm
package, and for the lightningcss platform binding (a compiled binary) the tagged lightningcss repository.

The Go side links only these modules, all permissive. `make licenses-go` builds the Linux and Windows
programs, reads their module lists with `go version -m`, and fails on a module that isn't in
`build/go-licenses.tsv` (so a new dependency has to be reviewed) or whose licence isn't permissive.

| module | version | licence | linked in |
|---|---|---|---|
| `golang.org/x/sys` | v0.49.0 | BSD-3-Clause | Windows |
| `fyne.io/systray` | v1.12.2 | Apache-2.0 | Windows and Linux (the tray icon) |
| `github.com/godbus/dbus/v5` | v5.1.0 | BSD-2-Clause | Linux (systray's D-Bus tray, and the check for a tray area); listed for Windows too |

On Linux no GUI library is linked: the tray speaks D-Bus in pure Go, and dialogs are the `zenity` or
`kdialog` program run as a separate process (both GPL, so they are not linked or shipped).

The npm side is checked by `build/licenses.mjs`, above.

## Open items

- The Windows installer has not yet been run on a real Windows machine. Before the first handover, run
  `Stagepack-Setup.exe` on one and open a `.sptx` file: install, double-click, presenter view, audience view,
  a notes edit saved into the file ("Notes saved"), Stop, uninstall. Until then Windows behaviour is tested
  only by cross-compiling and by the tests that don't need Windows.
