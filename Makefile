# Stagepack: pack a Slidev talk into one .sptx file.
# Go and Node are fetched once into .cache/ (pinned, checksummed): see build/fetch.sh.

GO := $(CURDIR)/.cache/go/bin/go
GOFMT := $(CURDIR)/.cache/go/bin/gofmt
include build/versions.env

# OS=linux|win32 picks the target of `runtime` and `node`
OS ?= linux
ifeq ($(OS),win32)
DIST_OS := windows
NODE_DIR := node-v$(NODE_VERSION)-win-x64
NODE_FETCH := node-win
else ifeq ($(OS),linux)
DIST_OS := linux
NODE_DIR := node-v$(NODE_VERSION)-linux-x64
NODE_FETCH := node-linux
else
$(error OS must be linux or win32, not $(OS))
endif

.PHONY: go node-linux node-win node runtime test e2e payload winres viewer setup build-linux build-windows licenses-go

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
	GOOS=$(DIST_OS) GOARCH=amd64 $(GO) build -o dist/$(DIST_OS)/stagepack$(if $(filter win32,$(OS)),.exe) ./cmd/stagepack

# dist/windows/stagepack.exe: the Windows viewer without a console, with icon and manifest. It carries no
# payload; the installer puts this file on the user's machine (the console build is `make build OS=win32`).
viewer: go winres
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "-H=windowsgui -s -w" -o dist/windows/stagepack.exe ./cmd/stagepack

# internal/install/payload.zip: the Windows node/ and runtimes/ and the viewer, to embed
# (needs `make node runtime OS=win32` and `make viewer`)
payload:
	build/payload.sh

# cmd/stagepack/rsrc_windows_amd64.syso: icon, manifest and version info for the Windows program.
# go-winres (0BSD, pinned in build/versions.env) is fetched on first use and runs at build time only.
winres: go
	$(GO) run ./build/mkwinres dist/winres assets/stagepack.ico
	cd cmd/stagepack && $(GO) run github.com/tc-hib/go-winres@$(WINRES_VERSION) make --in ../../dist/winres/winres.json --arch amd64

SETUP_NAME := Stagepack-Setup.exe

# dist/windows/$(SETUP_NAME): the installer. It holds the runtime and a copy of the viewer, and needs no console.
setup: viewer payload
	GOOS=windows GOARCH=amd64 $(GO) build -tags payload -trimpath -ldflags "-H=windowsgui -s -w" -o dist/windows/$(SETUP_NAME) ./cmd/stagepack

# The finished builds. The 600 MB node/ and runtimes/ are made only when they are missing (they depend on
# the directories, not on a phony rebuild); `make node runtime OS=...` forces them again.
LINUX_NODE := dist/linux/node/bin/node
LINUX_RUNTIME := dist/linux/runtimes/53/node_modules/@slidev/cli/bin/slidev.mjs
WIN_NODE := dist/windows/node/node.exe
WIN_RUNTIME := dist/windows/runtimes/53/node_modules/@slidev/cli/bin/slidev.mjs

$(LINUX_NODE):
	$(MAKE) node OS=linux

$(LINUX_RUNTIME):
	$(MAKE) runtime OS=linux

$(WIN_NODE):
	$(MAKE) node OS=win32

$(WIN_RUNTIME):
	$(MAKE) runtime OS=win32

# dist/linux/stagepack with node/ and runtimes/ beside it: the layout `stagepack open` and the e2e tests expect
build-linux: $(LINUX_NODE) $(LINUX_RUNTIME)
	$(MAKE) build OS=linux

# dist/windows/Stagepack-Setup.exe and PRESENTER.txt beside it (the guide to hand over with the installer)
build-windows: $(WIN_NODE) $(WIN_RUNTIME)
	$(MAKE) setup
	cp PRESENTER.txt dist/windows/PRESENTER.txt

# The licences of the Go modules the programs link; fails on one that isn't permissive or isn't in build/go-licenses.tsv
licenses-go: go
	build/go-licenses.sh
