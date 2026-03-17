.PHONY: build test run-server run-client cert lint

build:
	go build ./...

test:
	go test -race ./...

run-server:
	go run ./cmd/server -addr :8443 -token changeme

run-client:
	go run ./cmd/client -server localhost:8443 -token changeme -name client1

cert:
	go run ./cmd/server -gencert

lint:
	go vet ./...
