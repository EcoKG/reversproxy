#!/usr/bin/env bash
# reversproxy 서버/클라이언트를 Linux·Windows용으로 크로스컴파일합니다.
#
# 산출물 (dist/ 아래, .gitignore 처리됨):
#   dist/linux/reversproxy-server        Linux 서버       (CLI)
#   dist/linux/reversproxy-client        Linux 클라이언트  (CLI,  cmd/client)
#   dist/windows/reversproxy-server.exe  Windows 서버      (CLI)
#   dist/windows/reversproxy-client.exe  Windows 클라이언트 (GUI 트레이, cmd/winclient)
#
# CGO 없이(순수 Go) 빌드하므로 C 컴파일러 없이 어떤 호스트에서도 양쪽 OS 바이너리를 만들 수 있습니다.
#
# 사용법:
#   scripts/build.sh                        # Linux + Windows 모두
#   TARGET_OS=linux   scripts/build.sh      # Linux만
#   TARGET_OS=windows scripts/build.sh      # Windows만
#   ARCH=arm64        scripts/build.sh      # arm64 대상
#
# 주의: 변수명은 OS 가 아니라 TARGET_OS 입니다. 윈도우는 OS=Windows_NT 환경변수를
#       기본 제공하고 git-bash가 이를 상속하므로, OS 를 쓰면 충돌합니다.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGET_OS="${TARGET_OS:-all}"
ARCH="${ARCH:-amd64}"
export CGO_ENABLED=0

copy_configs() {
    local dest="$1"
    for name in server.yaml client.yaml; do
        if [ -f "release/$name" ]; then
            cp -f "release/$name" "$dest/$name"
        fi
    done
}

# build_one <goos> <pkg> <out> [ldflags]
# ldflags 기본값은 심볼 제거(-s -w). GUI 트레이는 '-s -w -H windowsgui'를 넘겨
# 실행 시 콘솔 창이 뜨지 않게 한다.
build_one() {
    local goos="$1" pkg="$2" out="$3" ldflags="${4:--s -w}"
    printf '  %-30s (%s/%s)  <- %s\n' "$(basename "$out")" "$goos" "$ARCH" "$pkg"
    GOOS="$goos" GOARCH="$ARCH" go build -trimpath -ldflags "$ldflags" -o "$out" "$pkg"
}

if [ "$TARGET_OS" = "all" ] || [ "$TARGET_OS" = "linux" ]; then
    mkdir -p dist/linux
    echo "Linux:"
    build_one linux ./cmd/server dist/linux/reversproxy-server
    build_one linux ./cmd/client dist/linux/reversproxy-client
    copy_configs dist/linux
fi

if [ "$TARGET_OS" = "all" ] || [ "$TARGET_OS" = "windows" ]; then
    mkdir -p dist/windows
    echo "Windows:"
    build_one windows ./cmd/server    dist/windows/reversproxy-server.exe
    build_one windows ./cmd/winclient dist/windows/reversproxy-client.exe '-s -w -H windowsgui'
    build_one windows ./cmd/winserver dist/windows/reversproxy-winserver.exe '-s -w -H windowsgui'
    copy_configs dist/windows
fi

# GitHub-release-named copies (match install scripts / .github/workflows/release.yml).
mkdir -p dist/release
[ -f dist/linux/reversproxy-server ]       && cp -f dist/linux/reversproxy-server       "dist/release/reversproxy-server-linux-$ARCH"
[ -f dist/linux/reversproxy-client ]       && cp -f dist/linux/reversproxy-client       "dist/release/reversproxy-client-linux-$ARCH"
[ -f dist/windows/reversproxy-server.exe ] && cp -f dist/windows/reversproxy-server.exe "dist/release/reversproxy-server-windows-$ARCH.exe"
[ -f dist/windows/reversproxy-client.exe ] && cp -f dist/windows/reversproxy-client.exe "dist/release/reversproxy-client-windows-$ARCH.exe"
[ -f dist/windows/reversproxy-winserver.exe ] && cp -f dist/windows/reversproxy-winserver.exe "dist/release/reversproxy-winserver-windows-$ARCH.exe"

echo
echo "완료. 산출물: $ROOT/dist"
find dist -type f ! -name '*.yaml' -printf '%10s  %p\n' 2>/dev/null || find dist -type f ! -name '*.yaml'
