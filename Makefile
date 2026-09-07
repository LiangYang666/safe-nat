# safe-nat build helpers.
BIN      := safenat
PKG      := ./cmd/safenat
LDFLAGS  := -s -w
GO       ?= go
NPM      := npm

.PHONY: all build test vet fmt-check web dist clean

all: build

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	$(GO) vet ./...

web:
	cd web && $(NPM) ci --include=dev && $(NPM) run build

# Quick cross-compile smoke targets (full matrix lives in CI release.yml).
dist:
	@for t in "linux amd64" "linux arm64" "linux 386" "darwin arm64" "windows amd64"; do \
		set -- $$t; os=$$1; arch=$$2; \
		ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo ">> $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o dist/safenat-$$os-$$arch$$ext $(PKG) || exit 1; \
	done

clean:
	rm -rf $(BIN) dist
