BINARY := codebridge
VERSION ?= 1.0.0-dev
LDFLAGS := -s -w -X codebridge/internal/app.Version=$(VERSION)
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin
DESTDIR ?=
INSTALL ?= install
NPM ?= npm
ADMIN_UI_DIR := web/admin

.PHONY: all build install test vet check admin-ui admin-ui-check clean run

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

vet:
	go vet ./...

check: test vet

admin-ui:
	cd $(ADMIN_UI_DIR) && $(NPM) ci && $(NPM) run build

admin-ui-check:
	cd $(ADMIN_UI_DIR) && $(NPM) ci && $(NPM) run check

run:
	go run ./cmd/codebridge serve --no-tunnel --port 8132

clean:
	go clean
	rm -rf dist
