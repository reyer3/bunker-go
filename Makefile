.PHONY: build test vet race fmt-check release release-test

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

# release publishes a sanitized snapshot to the public repo; see
# scripts/release.sh. Usage: make release VERSION=v0.2.0 [PUSH=--push]
release:
	@test -n "$(VERSION)" || { echo "usage: make release VERSION=vX.Y.Z [PUSH=--push]"; exit 1; }
	scripts/release.sh $(VERSION) $(PUSH)

release-test:
	scripts/release_test.sh
