# stagepack

[Stagepack](https://github.com/Eiriksb/stagepack) packs a [Slidev](https://sli.dev) talk into one `.sptx`
file that opens with a double-click, offline, in Slidev's presenter view. Edits to the speaker notes are
saved back into the file.

This package is a small launcher. The viewer, with its own Node and a pinned Slidev runtime, is too big for
npm (about 800 MB unpacked). So `stagepack install` downloads the build for your platform from the GitHub
release of the same version, checks it against the SHA-256 checksums shipped in this package, and installs
it for the current user only, without administrator rights:

```bash
npx stagepack install
```

or keep the command around:

```bash
npm install -g stagepack
stagepack install
```

Supported: Linux x86_64 (it installs to `~/.local/share/stagepack` and registers `.sptx` files; needs `tar`
and `xz`) and Windows x64 (it runs `Stagepack-Setup.exe`, which installs to `%LOCALAPPDATA%\Stagepack`).

After that, double-click a `.sptx` file to present it. The other commands run the installed viewer:

| command | what it does |
|---|---|
| `stagepack install [--yes]` | downloads and installs this version, or registers the file type again when it is installed already |
| `stagepack open talk.sptx` | what a double-click does |
| `stagepack pack DIR -o talk.sptx` | packs a Slidev project |
| `stagepack uninstall` | removes the installed viewer; your `.sptx` files are not touched |
| `stagepack version` | this launcher's version and the installed viewer's |

Uninstalling the npm package does not remove the viewer: run `stagepack uninstall` first.

MIT licensed. The viewer bundles Node and Slidev's npm packages; their licences are listed in the release.
