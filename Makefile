# 빌드 매트릭스
#                Linux                         Windows
#   server   dist/linux/reversproxy-server     dist/windows/reversproxy-server.exe   (둘 다 CLI)
#   client   dist/linux/reversproxy-client     dist/windows/reversproxy-client.exe
#            (CLI, cmd/client)                  (GUI 트레이, cmd/winclient)
#
# CGO 없이(순수 Go) 빌드하므로 C 컴파일러 없이 양쪽 OS를 크로스컴파일할 수 있습니다.

ARCH    ?= amd64
LDFLAGS := -s -w
GOBUILD := CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)'

.PHONY: build build-all test run-server run-client lint \
        dist dist-linux dist-windows \
        server-linux client-linux server-windows client-windows clean

# 현재 호스트 OS용 전체 빌드(검증용).
build:
	go build ./...

test:
	go test -race ./...

# 서버는 클라이언트로 dial 하고 공개 포트를 연다(데이터 :8444). TLS 인증서는
# 최초 실행 시 자동 생성되므로 별도 cert 타깃은 없다.
run-server:
	go run ./cmd/server -data-addr :8444 -token changeme

# 클라이언트는 listen 한다(서버가 접속). 다이얼하지 않으므로 -server 플래그는 없다.
run-client:
	go run ./cmd/client -listen :8443 -token changeme -name client1

lint:
	go vet ./...

# ── 크로스컴파일 ──────────────────────────────────────────────
# Linux + Windows 전부 빌드. (build-all 은 dist 의 별칭)
build-all: dist
dist: dist-linux dist-windows

dist-linux: server-linux client-linux
dist-windows: server-windows client-windows

server-linux:
	@mkdir -p dist/linux
	GOOS=linux GOARCH=$(ARCH) $(GOBUILD) -o dist/linux/reversproxy-server ./cmd/server

client-linux:
	@mkdir -p dist/linux
	GOOS=linux GOARCH=$(ARCH) $(GOBUILD) -o dist/linux/reversproxy-client ./cmd/client

server-windows:
	@mkdir -p dist/windows
	GOOS=windows GOARCH=$(ARCH) $(GOBUILD) -o dist/windows/reversproxy-server.exe ./cmd/server

# Windows 클라이언트는 GUI 시스템 트레이 버전(cmd/winclient)입니다.
# -H windowsgui: 실행 시 콘솔 창이 뜨지 않게 한다.
client-windows:
	@mkdir -p dist/windows
	GOOS=windows GOARCH=$(ARCH) CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS) -H windowsgui' -o dist/windows/reversproxy-client.exe ./cmd/winclient

clean:
	rm -rf dist
