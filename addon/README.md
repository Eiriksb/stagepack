# slidev-addon-stagepack

A [Slidev](https://sli.dev) addon that adds an **Export as .sptx** button to Slidev's nav bar. One click packs
the presentation into a single `.sptx` file and your browser downloads it.
[Stagepack](https://github.com/Eiriksb/stagepack) opens that file with a double-click, offline, in Slidev's
presenter view, and saves the speaker's note edits back into it. The presenter needs no Node or npm.

## Use

1. Install Stagepack once; the addon packs with it:

   ```bash
   npx stagepack install
   ```

   (Or the Windows installer or the Linux install script: see
   [Install](https://github.com/Eiriksb/stagepack#install).)

2. Add the addon to your presentation:

   ```bash
   npm i -D slidev-addon-stagepack
   ```

   and name it in the headmatter of `slides.md`:

   ```yaml
   ---
   addons: [stagepack]
   ---
   ```

3. Run `slidev` as usual and click the box icon in the nav bar (bottom left in play mode, or the presenter's
   toolbar). The `.sptx` file lands in your downloads. Notes from the packer, for example about fonts that would
   need the internet, are shown next to the button.

The button is there only on `slidev`'s dev server: a built site (`slidev build`) has no server to pack with, so it
has no button. In Stagepack's viewer, which bundles this addon so that presentations naming it open there, the
addon does nothing either.

## How it works

`setup/vite-plugins.ts` adds two endpoints to the dev server: `GET /__stagepack/status` and
`POST /__stagepack/export`. The export runs Stagepack's own packer,
`stagepack pack <presentation folder> -o <temp file> --entry <slides file>`, and sends back the file; it answers
only requests from the Slidev page itself. The packer is found as `$STAGEPACK_BIN`, else the per-user install
(`~/.local/share/stagepack/stagepack`, `%LOCALAPPDATA%\Stagepack\stagepack.exe`), else `stagepack` on `PATH`.

Naming exactly `stagepack` or `slidev-addon-stagepack` in `addons:` does not make Stagepack's viewer ask the
"contains code" question, since the viewer brings its own copy. Any other addon still does. Write it as
`addons: [stagepack]` on one line, or as `addons:` with `  - stagepack` on the next, so that `stagepack pack`'s
quick check recognises it too.

Needs Slidev 53 or later. MIT licensed.
