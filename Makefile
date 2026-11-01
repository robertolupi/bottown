.PHONY: build install test e2e vet check web

# The web UI, built by vite into internal/webui/dist/ui, which is not
# tracked. Needs Node and npm. make build does not depend on it: without
# it the binary serves a stub page that says to run make web. make install
# builds it.
web: web/node_modules
	cd web && npm run build

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch $@

VERSION ?= $(shell git describe --always --dirty --abbrev=12 2>/dev/null || echo dev)
LDFLAGS := -X github.com/robertolupi/bottown/internal/cli.Version=$(VERSION)
BINDIR  ?= $(HOME)/.local/bin

build:
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o bin/bottown ./cmd/bottown

# The one step after pulling a change: build the web UI, install the CLI,
# refresh the harness slash commands, and upgrade the prod server when HEAD is bottown/main.
#
# The binary is built to a temporary name and renamed: overwriting a running
# binary in place can get it killed on macOS. In each fam's main, re-run
# `bottown actor setup <actors>` when BINDIR changes: it records this
# binary's absolute path (harness setup, which does too, runs every time).
#
# A linked worktree (an actor's, or a run's) is refused: the installed binary
# is shared by every actor. The server step runs only on the commit
# bottown/main points at, the remote `bottown clone` sets up for the forge,
# and skips building when the commits differ only in docs (up --if-changed).
# UP=0 skips it.
UP ?= 1

install:
	@if [ "$$(git rev-parse --git-dir)" != "$$(git rev-parse --git-common-dir)" ] && [ -z "$(FORCE)" ]; then \
	  echo "refusing to install from a linked worktree: it replaces the shared $(BINDIR)/bottown."; \
	  echo "use make build (bin/bottown), or make install FORCE=1"; exit 1; fi
	$(MAKE) web
	mkdir -p $(BINDIR)
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BINDIR)/.bottown.new ./cmd/bottown
	mv $(BINDIR)/.bottown.new $(BINDIR)/bottown
	@echo "installed $(BINDIR)/bottown ($(VERSION))"
	$(BINDIR)/bottown harness setup
	@main=$$(git rev-parse -q --verify refs/remotes/bottown/main); head=$$(git rev-parse HEAD); \
	if [ "$(UP)" = 0 ]; then echo "skipping the prod server: UP=0"; \
	elif [ -z "$$main" ]; then echo "skipping the prod server: no bottown/main (not a fam's main checkout)"; \
	elif [ "$$head" != "$$main" ]; then echo "skipping the prod server: HEAD $$head is not bottown/main $$main"; \
	else echo "$(BINDIR)/bottown up -i prod --ref $$head --if-changed"; \
	  $(BINDIR)/bottown up -i prod --ref $$head --if-changed; fi

vet:
	go vet ./... && go vet -tags e2e ./e2e/

test:
	go test ./...

# The repository's checks, run before a PR and by verifiers: gofmt clean,
# go.mod/go.sum tidy, vet (incl. the e2e build tag) and the unit tests.
check: vet test
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	go mod tidy -diff

# Needs Docker. Uses throwaway e2e-* instances; never touches prod or dev.
e2e:
	go test -tags e2e -count=1 -timeout 30m -v ./e2e/
