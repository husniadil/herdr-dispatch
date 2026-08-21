.PHONY: build test test-full install clean

BIN := bin/hdis

build:
	go build -o $(BIN) ./cmd/hdis

# Two gates, one suite — the same split herdr-tasks uses, for the same reason:
#
#   make test       the loop, in seconds — the pure decision core and the
#                   payload shapes, no processes spawned.
#   make test-full  the gate before a commit — the above plus every case that
#                   shells out to a fake htask or a fake herdr on PATH, with
#                   -race and a cross-compile vet of the other platform.
#
# gofmt is checked rather than applied: a formatting fix belongs in the commit
# that caused it, not silently in whoever runs the gate next.
test:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed: $$unformatted"; exit 1; fi
	go test ./...

test-full: test
	go vet ./...
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=darwin GOARCH=arm64 go vet ./...
	go test -race ./...

install: build
	cp $(BIN) $(shell go env GOPATH)/bin/hdis

clean:
	rm -rf bin dist coverage.out
