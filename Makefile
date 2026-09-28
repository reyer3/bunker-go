.PHONY: build test vet race fmt-check install dev hooks denylist

# All targets always build with -tags goolm for the pure-Go Matrix
# adapter (mautrix-go built with goolm, no libolm/CGO). build additionally
# pins CGO_ENABLED=0, the acceptance check for "pure Go, no CGO": every
# store (modernc.org/sqlite) and crypto path here is already CGO-free, so
# this only turns a silent regression into a build failure.

build:
	CGO_ENABLED=0 go build -tags goolm ./...

test:
	go test -tags goolm ./...

vet:
	go vet -tags goolm ./...

race:
	go test -tags goolm -race ./...

fmt-check:
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "gofmt needs to be run on:"; \
		gofmt -l .; \
		exit 1; \
	fi

# install builds bunker into $(PREFIX)/bin, where deploy/systemd's unit
# expects it. dev also restarts the daemon when it runs as that user
# service, so "git pull && make dev" is the whole local test loop.
PREFIX ?= $(HOME)/.local

install:
	CGO_ENABLED=0 go build -tags goolm -trimpath -o $(PREFIX)/bin/bunker ./cmd/bunker

dev: install
	@if systemctl --user is-active --quiet bunker 2>/dev/null; then \
		systemctl --user restart bunker && echo "bunker.service restarted"; \
	else \
		echo "installed $(PREFIX)/bin/bunker; restart your 'bunker daemon' to pick it up"; \
	fi

# hooks installs the pre-commit denylist check (scripts/check-denylist.sh).
hooks:
	ln -sf ../../scripts/pre-commit .git/hooks/pre-commit
	@echo "pre-commit hook installed"

# denylist checks every tracked file against the local denylist.
denylist:
	scripts/check-denylist.sh
