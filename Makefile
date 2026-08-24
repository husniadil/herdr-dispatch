.PHONY: build test test-full install clean

BIN := bin/hdis

build:
	go build -o $(BIN) ./cmd/hdis

# Two gates, one suite — the same split herdr-tasks uses, for the same reason:
#
#   make test       the loop, in seconds — the pure decision core and the
#                   payload shapes, no processes spawned. -short is what makes
#                   that true: the cases that build the binary skip on it.
#   make test-full  the gate before a commit — the above plus every case that
#                   shells out to a fake htask or a fake herdr on PATH, with
#                   -race and a cross-compile vet of the other platform.
#
# gofmt is checked rather than applied: a formatting fix belongs in the commit
# that caused it, not silently in whoever runs the gate next.
test:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed: $$unformatted"; exit 1; fi
	go test -short ./...

test-full: test
	go vet ./...
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	go test -race ./...

# Layer 3, and deliberately OUT of the gate above: it drives the shipped
# binary against a REAL `htask` built from the sibling herdr-tasks checkout,
# which CI does not have. A machine without that checkout gets a loud skip
# naming what was missing, never a silent pass. Run it before a release tag,
# and whenever the board adapter or the `htask <verb> --json` surface moves.
.PHONY: e2e
e2e: build
	go test -tags e2e -count=1 -v ./internal/e2e/...

# `go install`, not a copy into $GOPATH/bin. GOBIN is what decides where an
# installed binary lands, and a toolchain manager sets it away from GOPATH:
# measured on this machine, GOBIN was the mise toolchain's bin (on PATH) while
# $GOPATH/bin was ~/go/bin (not on PATH), so the copy put `hdis` somewhere
# nothing could run it. An agent that cannot reach the CLI is not choosing the
# MCP door; it is being handed one surface.
# The same layer 3, with the skip turned into a failure. A release must not be
# cut on a suite that silently did not run, so this is what goes before a tag —
# and it is the target herdr-tasks spells the same way.
.PHONY: release-check
release-check: test-full build
	DISPATCH_E2E_REQUIRED=1 go test -tags e2e -count=1 -v ./internal/e2e/...

install:
	go install ./cmd/hdis

clean:
	rm -rf bin dist coverage.out
