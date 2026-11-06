# Stagepack: pack a Slidev talk into one .sptx file.
# Go, Node and ffmpeg are fetched once into .cache/ (pinned, checksummed): see build/fetch.sh.

GO := $(CURDIR)/.cache/go/bin/go
GOFMT := $(CURDIR)/.cache/go/bin/gofmt
include build/versions.env

# OS=linux|win32 picks the target of `runtime`, `node` and `programs`
OS ?= linux
ifeq ($(OS),win32)
DIST_OS := windows
NODE_DIR := node-v$(NODE_VERSION)-win-x64
NODE_FETCH := node-win
FFMPEG_FETCH := ffmpeg-win
FFMPEG_DIR := $(basename $(notdir $(FFMPEG_URL_WIN_X64)))
EXE := .exe
else ifeq ($(OS),linux)
DIST_OS := linux
NODE_DIR := node-v$(NODE_VERSION)-linux-x64
NODE_FETCH := node-linux
FFMPEG_FETCH := ffmpeg-linux
FFMPEG_DIR := $(basename $(basename $(notdir $(FFMPEG_URL_LINUX_X64))))
EXE :=
else
$(error OS must be linux or win32, not $(OS))
endif

.PHONY: go node-linux node-win node ffmpeg-linux ffmpeg-win programs sources runtime test e2e payload winres viewer setup build-linux build-windows licenses-go release gh-release

# VERSION (0.1.0, no "v") is stamped into the programs (`stagepack version`, the Windows version info and
# uninstall entry). `make release VERSION=0.1.0` sets it; other builds are "dev".
VERSION ?=
GO_LDFLAGS := $(if $(VERSION),-X stagepack/internal/version.Version=$(VERSION))

go:
	build/fetch.sh go

node-linux:
	build/fetch.sh node-linux

node-win:
	build/fetch.sh node-win

# dist/<os>/node/: node.exe at the top on Windows, bin/node on Linux (Node's own layouts)
node: $(NODE_FETCH)
	rm -rf dist/$(DIST_OS)/node
	mkdir -p dist/$(DIST_OS)
	cp -a .cache/$(NODE_DIR) dist/$(DIST_OS)/node

ffmpeg-linux ffmpeg-win:
	build/fetch.sh $@

# dist/<os>/ffmpeg/: ffmpeg[.exe], its LICENSE.txt, and LIBRARY-LICENSES.txt, the licence texts of the libraries
# linked into it (build/ffmpeg-licenses.txt). Copied from .cache/ each time, so it is always the one
# build/versions.env pins.
programs: $(FFMPEG_FETCH)
	rm -rf dist/$(DIST_OS)/ffmpeg
	mkdir -p dist/$(DIST_OS)/ffmpeg
	cp .cache/$(FFMPEG_DIR)/bin/ffmpeg$(EXE) .cache/$(FFMPEG_DIR)/LICENSE.txt dist/$(DIST_OS)/ffmpeg/
	cp build/ffmpeg-licenses.txt dist/$(DIST_OS)/ffmpeg/LIBRARY-LICENSES.txt

# .cache/dl/: the source archives a release mirrors (ffmpeg, BtbN's build scripts and the libraries linked into
# ffmpeg; see build/programs.tsv). Online, about 1.2 GB the first time; needs git, and svn for xvid.
sources:
	build/fetch.sh sources
	build/fetch.sh ffmpeg-libraries

# dist/<os>/runtimes/53/: node_modules, package.json, pnpm-workspace.yaml, THIRD_PARTY_LICENSES.txt
runtime:
	build/runtime.sh $(OS)

test: go
	@out=$$($(GOFMT) -l cmd internal runtime $$(test -d test && echo test) 2>&1); \
	if [ -n "$$out" ]; then echo "gofmt needed on:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...
	$(GO) test ./...

# Runs the e2e-tagged tests; they skip when dist/linux (make runtime node OS=linux) is missing.
e2e: go
	$(GO) test -tags e2e ./...

# dist/<os>/stagepack[.exe]: the viewer itself (the Windows one is cross-compiled)
.PHONY: build
build: go
	GOOS=$(DIST_OS) GOARCH=amd64 $(GO) build -ldflags "$(GO_LDFLAGS)" -o dist/$(DIST_OS)/stagepack$(if $(filter win32,$(OS)),.exe) ./cmd/stagepack

# dist/windows/stagepack.exe: the Windows viewer without a console, with icon and manifest. It carries no
# payload; the installer puts this file on the user's machine (the console build is `make build OS=win32`).
viewer: go winres
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "-H=windowsgui -s -w $(GO_LDFLAGS)" -o dist/windows/stagepack.exe ./cmd/stagepack

# internal/install/payload.zip: the Windows node/, runtimes/ and ffmpeg/ and the viewer, to embed
# (needs `make node runtime programs OS=win32` and `make viewer`)
payload:
	build/payload.sh

# cmd/stagepack/rsrc_windows_amd64.syso: icon, manifest and version info for the Windows program.
# go-winres (0BSD, pinned in build/versions.env) is fetched on first use and runs at build time only.
winres: go
	$(GO) run ./build/mkwinres dist/winres assets/stagepack.ico $(or $(VERSION),dev)
	cd cmd/stagepack && $(GO) run github.com/tc-hib/go-winres@$(WINRES_VERSION) make --in ../../dist/winres/winres.json --arch amd64

SETUP_NAME := Stagepack-Setup.exe

# dist/windows/$(SETUP_NAME): the installer. It holds the runtime, the programs and a copy of the viewer, and needs
# no console. build/programs.sh checks the programs and adds their licences before the payload is zipped.
setup: viewer
	$(MAKE) programs OS=win32
	build/programs.sh dist/windows
	build/payload.sh
	GOOS=windows GOARCH=amd64 $(GO) build -tags payload -trimpath -ldflags "-H=windowsgui -s -w $(GO_LDFLAGS)" -o dist/windows/$(SETUP_NAME) ./cmd/stagepack

# The finished builds. The 600 MB node/ is made only when it is missing, and the runtime when it is missing or its
# .runtime-stamp (build/runtime-stamp.sh) is not that of runtime/ now; `make node runtime OS=...` forces them again. ffmpeg/ is copied
# from .cache/ each time (`make programs`), and build/programs.sh checks the programs beside the viewer.
LINUX_NODE := dist/linux/node/bin/node
LINUX_RUNTIME := linux-runtime
WIN_NODE := dist/windows/node/node.exe
WIN_RUNTIME := win-runtime

$(LINUX_NODE):
	$(MAKE) node OS=linux

.PHONY: $(LINUX_RUNTIME) $(WIN_RUNTIME)
$(LINUX_RUNTIME):
	build/runtime-stamp.sh check linux || $(MAKE) runtime OS=linux

$(WIN_NODE):
	$(MAKE) node OS=win32

$(WIN_RUNTIME):
	build/runtime-stamp.sh check win32 || $(MAKE) runtime OS=win32

# dist/linux/stagepack with node/, runtimes/ and ffmpeg/ beside it: the layout `stagepack open`,
# `stagepack export-pptx` and the e2e tests expect
build-linux: $(LINUX_NODE) $(LINUX_RUNTIME)
	build/addon.sh linux
	$(MAKE) programs OS=linux
	build/programs.sh dist/linux
	$(MAKE) build OS=linux

# dist/windows/Stagepack-Setup.exe and PRESENTER.txt beside it (the guide to hand over with the installer)
build-windows: $(WIN_NODE) $(WIN_RUNTIME)
	build/addon.sh win32
	$(MAKE) setup
	cp PRESENTER.txt dist/windows/PRESENTER.txt

# The licences of the Go modules the programs link; fails on one that isn't permissive or isn't in build/go-licenses.tsv
licenses-go: go
	build/go-licenses.sh

# dist/release/v$(VERSION)/: what a GitHub release carries (Stagepack-Setup.exe, PRESENTER.txt, the Linux
# tarball stagepack-$(VERSION)-linux-x64.tar.xz, the bundled programs' archives and their sources, SHA256SUMS)
# and the npm package (npm/stagepack-$(VERSION).tgz, with SHA256SUMS inside). Builds both programs with VERSION
# stamped in; Node and the runtime are made only when missing, as in build-linux and build-windows. Refuses a
# tree with uncommitted changes (ALLOW_DIRTY=1).
release: go
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=0.1.0" >&2; exit 2; }
	build/release.sh check $(VERSION)
	$(MAKE) test licenses-go
	$(MAKE) build-linux build-windows VERSION=$(VERSION)
	$(MAKE) sources
	build/release.sh pack $(VERSION)

# Publishes dist/release/v$(VERSION) as the GitHub release v$(VERSION) (tags the current commit). Online.
gh-release:
	@test -n "$(VERSION)" || { echo "usage: make gh-release VERSION=0.1.0" >&2; exit 2; }
	build/release.sh github $(VERSION)
