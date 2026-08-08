.PHONY: build test vet cross

build:
	go build -trimpath -o proxyforge ./cmd/proxyforge

test:
	go test ./...

vet:
	go vet ./...

cross:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 go build -trimpath -o dist/proxyforge-linux-amd64 ./cmd/proxyforge
	GOOS=linux GOARCH=arm64 go build -trimpath -o dist/proxyforge-linux-arm64 ./cmd/proxyforge
