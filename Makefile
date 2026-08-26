# Storix build.
# Developed by X Project.

BINARY      := storix
MODULE      := github.com/XProject25/Storix
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE  ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PREFIX      ?= /usr
DIST        := dist
WEB_DIST    := internal/web/dist

LDFLAGS := -s -w \
	-X '$(MODULE)/internal/build.Version=$(VERSION)' \
	-X '$(MODULE)/internal/build.Commit=$(COMMIT)' \
	-X '$(MODULE)/internal/build.Date=$(BUILD_DATE)'

GO      ?= go
GOFLAGS := -trimpath
export CGO_ENABLED = 0

.PHONY: all build web backend clean test vet fmt lint dev run install uninstall release check tidy

## Build the interface and the binary.
all: build

build: web backend

## Compile the frontend into the folder the binary embeds.
web:
	@echo "  building the interface"
	@cd web && (test -d node_modules || npm ci --no-audit --no-fund) && npm run build

## Compile the binary with the interface already embedded.
backend:
	@echo "  building $(BINARY) $(VERSION)"
	@$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/storix
	@echo "  done: ./$(BINARY)"

## Run the tests.
test:
	@$(GO) test ./... -count=1

## Static analysis.
vet:
	@$(GO) vet ./...

fmt:
	@gofmt -w $(shell find . -name '*.go' -not -path './web/*')

lint: vet
	@cd web && npx tsc --noEmit

check: vet test lint

tidy:
	@$(GO) mod tidy

## Run the API and the interface with hot reload, on ports 8686 and 5173.
dev:
	@echo "  API on http://localhost:8686, interface on http://localhost:5173"
	@$(GO) run ./cmd/storix serve -config ./storix.dev.yaml -data ./.devdata -port 8686 & \
	cd web && npm run dev

## Run the compiled binary against a local data directory.
run: backend
	@./$(BINARY) serve -config ./storix.dev.yaml -data ./.devdata

## Install onto this machine and register the service.
install: build
	@test "$$(id -u)" = "0" || (echo "  run: sudo make install"; exit 1)
	@install -o root -g root -m 0755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@id -u storix >/dev/null 2>&1 || useradd --system --no-create-home \
		--home-dir /var/lib/storix --shell /usr/sbin/nologin storix
	@mkdir -p /etc/storix /var/lib/storix /var/log/storix
	@chown -R storix:storix /var/lib/storix /var/log/storix
	@$(PREFIX)/bin/$(BINARY) config -init -config /etc/storix/config.yaml
	@install -m 0644 packaging/storix.service /etc/systemd/system/storix.service
	@systemctl daemon-reload && systemctl enable --now storix
	@echo "  installed. Setup link: sudo storix setup-token"

uninstall:
	@test "$$(id -u)" = "0" || (echo "  run: sudo make uninstall"; exit 1)
	@bash scripts/uninstall.sh

## Cross compile the release artifacts with their checksums.
release: web
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for target in linux/amd64 linux/arm64 linux/arm; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "  building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) ./cmd/storix; \
		tar -czf $(DIST)/$(BINARY)_$(VERSION)_$${os}_$${arch}.tar.gz -C $(DIST) $(BINARY); \
		rm -f $(DIST)/$(BINARY); \
	done
	@cd $(DIST) && sha256sum *.tar.gz > checksums.txt
	@echo "  artifacts in $(DIST)/"
	@ls -1 $(DIST)

clean:
	@rm -rf $(BINARY) $(DIST) $(WEB_DIST)/assets $(WEB_DIST)/*.png $(WEB_DIST)/*.webmanifest .devdata
	@echo "  cleaned"
