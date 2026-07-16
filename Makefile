BINARY := codebridge
VERSION ?= 0.1.0-dev
LDFLAGS := -s -w -X codebridge/internal/app.Version=$(VERSION)

.PHONY: all build test vet check clean run

all: check build

build:
	mkdir -p dist
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/codebridge

test:
	go test ./...

vet:
	go vet ./...

check: test vet

run:
	go run ./cmd/codebridge serve --no-tunnel

clean:
	go clean
	rm -rf dist
