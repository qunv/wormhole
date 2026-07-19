BINARY := codebridge
VERSION ?= 0.1.0-dev
LDFLAGS := -s -w -X codebridge/internal/app.Version=$(VERSION)
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
INSTALL ?= install

.PHONY: all build install test test-database-integration vet check clean run

all: check build

build:
	mkdir -p dist
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY) ./cmd/codebridge

install: build
	$(INSTALL) -Dm755 dist/$(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
	@echo "Installed $(BINARY) to $(DESTDIR)$(BINDIR)/$(BINARY)"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "Note: add $(BINDIR) to your PATH if '$(BINARY)' is not found."; esac

test:
	go test ./...

test-database-integration:
	./scripts/test-database-integration.sh

vet:
	go vet ./...

check: test vet

run:
	go run ./cmd/codebridge serve --no-tunnel

clean:
	go clean
	rm -rf dist
