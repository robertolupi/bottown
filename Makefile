.PHONY: build install test e2e vet check web

web:
	cd web && npm run build

VERSION ?= $(shell git describe --always --dirty --abbrev=12 2>/dev/null || echo dev)
LDFLAGS := -X github.com/robertolupi/bottown/internal/cli.Version=$(VERSION)
BINDIR  ?= $(HOME)/.local/bin

build:
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o bin/bottown ./cmd/bottown

# Build to a temporary name and rename: overwriting a running binary in
# place can get it killed on macOS. Re-run `bottown harness setup` and, in
# each fam's main, `bottown actor setup <actors>` when BINDIR changes: they
# record this binary's absolute path.
install:
	mkdir -p $(BINDIR)
	go build -buildvcs=false -ldflags "$(LDFLAGS)" -o $(BINDIR)/.bottown.new ./cmd/bottown
	mv $(BINDIR)/.bottown.new $(BINDIR)/bottown
	@echo "installed $(BINDIR)/bottown ($(VERSION))"

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
