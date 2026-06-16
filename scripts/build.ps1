<#
.SYNOPSIS
  reversproxy 서버/클라이언트를 Linux·Windows용으로 크로스컴파일합니다.

.DESCRIPTION
  산출물 (dist/ 아래, .gitignore 처리됨):
    dist/linux/reversproxy-server        Linux 서버   (CLI)
    dist/linux/reversproxy-client        Linux 클라이언트 (CLI,  cmd/client)
    dist/windows/reversproxy-server.exe  Windows 서버 (CLI)
    dist/windows/reversproxy-client.exe  Windows 클라이언트 (GUI 트레이, cmd/winclient)

  CGO 없이(순수 Go) 빌드하므로 C 컴파일러가 필요 없으며, 어떤 호스트에서도
  양쪽 OS 바이너리를 만들 수 있습니다.

.PARAMETER Os
  대상 OS: all(기본) | linux | windows

.PARAMETER Arch
  대상 아키텍처 (기본: amd64). 예: arm64

.EXAMPLE
  ./scripts/build.ps1                 # Linux + Windows 모두 빌드
  ./scripts/build.ps1 -Os windows     # Windows만
  ./scripts/build.ps1 -Arch arm64     # arm64 대상
#>
param(
    [ValidateSet('all', 'linux', 'windows')]
    [string]$Os = 'all',
    [string]$Arch = 'amd64'
)

$ErrorActionPreference = 'Stop'

# 저장소 루트 = 이 스크립트의 부모 디렉터리.
$root = Split-Path -Parent $PSScriptRoot

# 릴리스용 example 설정 파일을 대상 디렉터리로 복사 (있을 때만).
function Copy-Configs([string]$destDir) {
    foreach ($name in @('server.yaml', 'client.yaml')) {
        $src = Join-Path $root "release/$name"
        if (Test-Path $src) {
            Copy-Item $src (Join-Path $destDir $name) -Force
        }
    }
}

# 단일 바이너리 빌드. 실패 시 throw 하여 전체 중단.
# $ldflags 기본값은 심볼 제거(-s -w). GUI 트레이는 '-H windowsgui'를 더해
# 실행 시 콘솔 창이 뜨지 않게 한다.
function Build-One([string]$goos, [string]$goarch, [string]$pkg, [string]$outPath, [string]$ldflags = '-s -w') {
    $env:GOOS = $goos
    $env:GOARCH = $goarch
    Write-Host ("  {0,-30} ({1}/{2})  <- {3}" -f (Split-Path -Leaf $outPath), $goos, $goarch, $pkg) -ForegroundColor Cyan
    & go build -trimpath -ldflags $ldflags -o $outPath $pkg
    if ($LASTEXITCODE -ne 0) { throw "go build 실패: $pkg ($goos/$goarch)" }
}

Push-Location $root
try {
    $env:CGO_ENABLED = '0'

    if ($Os -eq 'all' -or $Os -eq 'linux') {
        $d = Join-Path $root 'dist/linux'
        New-Item -ItemType Directory -Force -Path $d | Out-Null
        Write-Host 'Linux:' -ForegroundColor Green
        Build-One 'linux' $Arch './cmd/server' (Join-Path $d 'reversproxy-server')
        Build-One 'linux' $Arch './cmd/client' (Join-Path $d 'reversproxy-client')
        Copy-Configs $d
    }

    if ($Os -eq 'all' -or $Os -eq 'windows') {
        $d = Join-Path $root 'dist/windows'
        New-Item -ItemType Directory -Force -Path $d | Out-Null
        Write-Host 'Windows:' -ForegroundColor Green
        Build-One 'windows' $Arch './cmd/server'    (Join-Path $d 'reversproxy-server.exe')
        Build-One 'windows' $Arch './cmd/winclient' (Join-Path $d 'reversproxy-client.exe') '-s -w -H windowsgui'
        Copy-Configs $d
    }

    # GitHub-release-named copies (match install scripts / .github/workflows/release.yml).
    $rel = Join-Path $root 'dist/release'
    New-Item -ItemType Directory -Force -Path $rel | Out-Null
    $pairs = @(
        @{ src = "dist/linux/reversproxy-server";       dst = "reversproxy-server-linux-$Arch" },
        @{ src = "dist/linux/reversproxy-client";       dst = "reversproxy-client-linux-$Arch" },
        @{ src = "dist/windows/reversproxy-server.exe"; dst = "reversproxy-server-windows-$Arch.exe" },
        @{ src = "dist/windows/reversproxy-client.exe"; dst = "reversproxy-client-windows-$Arch.exe" }
    )
    foreach ($p in $pairs) {
        $s = Join-Path $root $p.src
        if (Test-Path $s) { Copy-Item $s (Join-Path $rel $p.dst) -Force }
    }

    Write-Host ''
    Write-Host "완료. 산출물: $(Join-Path $root 'dist')" -ForegroundColor Green
    Get-ChildItem -Path (Join-Path $root 'dist') -Recurse -File |
        Where-Object { $_.Name -notlike '*.yaml' } |
        ForEach-Object {
            "{0,10:N0} KB  {1}" -f ($_.Length / 1KB), $_.FullName.Substring($root.Length + 1)
        }
}
finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
