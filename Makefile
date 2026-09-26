.PHONY: build test vet race fmt-check

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
