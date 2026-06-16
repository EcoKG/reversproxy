.PHONY: build test run-server run-client cert lint winclient winserver

VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GIT_COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
WINSRV_LDFLAGS = -H windowsgui \
	-X main.Version=$(VERSION) -X main.Commit=$(GIT_COMMIT) -X main.BuildTime=$(BUILD_TIME)

build:
	go build ./...

# Cross-compile the Windows tray CLIENT (system tray + lxn/walk console).
# -H windowsgui suppresses the console window. Verify the GUI on Windows.
winclient:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="-H windowsgui" -o dist/winclient.exe ./cmd/winclient/

# Cross-compile the Windows tray SERVER (runs the server in the user session
# with a tray icon + native management console). Verify the GUI on Windows.
winserver:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags="$(WINSRV_LDFLAGS)" -o dist/winserver.exe ./cmd/winserver/

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
