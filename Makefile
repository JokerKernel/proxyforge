.PHONY: build test vet cross version

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
OUTPUT ?= proxyforge

build:
	VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_DATE="$(BUILD_DATE)" OUTPUT="$(OUTPUT)" ./scripts/build.sh

test:
	go test ./...

vet:
	go vet ./...

cross:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_DATE="$(BUILD_DATE)" OUTPUT=dist/proxyforge-linux-amd64 ./scripts/build.sh
	GOOS=linux GOARCH=arm64 VERSION="$(VERSION)" COMMIT="$(COMMIT)" BUILD_DATE="$(BUILD_DATE)" OUTPUT=dist/proxyforge-linux-arm64 ./scripts/build.sh

version:
	@printf 'VERSION=%s\nCOMMIT=%s\nBUILD_DATE=%s\n' "$(VERSION)" "$(COMMIT)" "$(BUILD_DATE)"
