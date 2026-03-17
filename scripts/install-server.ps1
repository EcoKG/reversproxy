#Requires -RunAsAdministrator
<#
.SYNOPSIS
    Install reversproxy server as a Windows service.
.USAGE
    # PowerShell (관리자 권한):
    irm https://raw.githubusercontent.com/EcoKG/reversproxy/master/scripts/install-server.ps1 | iex
#>

$ErrorActionPreference = "Stop"
$Repo = "EcoKG/reversproxy"
$InstallDir = "$env:ProgramFiles\reversproxy"
$ServiceName = "reversproxy-server"
$BinaryName = "reversproxy-server.exe"
$ConfigName = "server.yaml"

Write-Host "==> reversproxy server installer for Windows" -ForegroundColor Cyan

# Create install directory
if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

# Download latest binary
$ReleasesApi = "https://api.github.com/repos/$Repo/releases/latest"
try {
    $Release = Invoke-RestMethod -Uri $ReleasesApi
    $Asset = $Release.assets | Where-Object { $_.name -like "*server-windows*" } | Select-Object -First 1
    $DownloadUrl = $Asset.browser_download_url
    $Version = $Release.tag_name
} catch {
    Write-Host "==> Could not fetch latest release, using direct URL" -ForegroundColor Yellow
    $DownloadUrl = "https://github.com/$Repo/releases/latest/download/reversproxy-server-windows-amd64.exe"
    $Version = "latest"
}

Write-Host "==> Downloading $Version from $DownloadUrl"
$BinaryPath = Join-Path $InstallDir $BinaryName
Invoke-WebRequest -Uri $DownloadUrl -OutFile $BinaryPath

# Create default config if not exists
$ConfigPath = Join-Path $InstallDir $ConfigName
if (-not (Test-Path $ConfigPath)) {
    Write-Host "==> Creating default config at $ConfigPath"
    @"
# reversproxy server configuration
data_addr: ":8444"
http_addr: ":8080"
https_addr: ":8445"
admin_addr: ":9090"
auth_token: "changeme"
cert_path: "server.crt"
key_path: "server.key"
log_level: "info"

# List of clients to connect to
clients:
  - name: "client1"
    address: "192.168.1.10:8443"
    auth_token: "changeme"
  # - name: "client2"
  #   address: "192.168.1.20:8443"
  #   auth_token: "changeme"
"@ | Set-Content -Path $ConfigPath -Encoding UTF8
}

# Stop existing service if running
$existingService = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existingService) {
    Write-Host "==> Stopping existing service"
    Stop-Service -Name $ServiceName -Force -ErrorAction SilentlyContinue
    & sc.exe delete $ServiceName 2>$null
    Start-Sleep -Seconds 2
}

# Install as Windows service using sc.exe
Write-Host "==> Installing Windows service: $ServiceName"
$BinPathArg = "`"$BinaryPath`" --config `"$ConfigPath`""
& sc.exe create $ServiceName binPath= $BinPathArg start= auto displayname= "ReverseProxy Tunnel Server"
& sc.exe description $ServiceName "Reverse tunnel proxy server - connects to clients and manages tunnels"
& sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/10000/restart/30000

# Add firewall rules
Write-Host "==> Adding firewall rules"
$ports = @(
    @{Name="reversproxy-data";    Port=8444; Desc="ReverseProxy data connections"},
    @{Name="reversproxy-http";    Port=8080; Desc="ReverseProxy HTTP proxy"},
    @{Name="reversproxy-https";   Port=8445; Desc="ReverseProxy HTTPS proxy"},
    @{Name="reversproxy-admin";   Port=9090; Desc="ReverseProxy admin API"}
)
foreach ($p in $ports) {
    $existing = Get-NetFirewallRule -DisplayName $p.Name -ErrorAction SilentlyContinue
    if (-not $existing) {
        New-NetFirewallRule -DisplayName $p.Name -Direction Inbound -Protocol TCP -LocalPort $p.Port -Action Allow -Description $p.Desc | Out-Null
    }
}

# Add to PATH
$machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
if ($machinePath -notlike "*$InstallDir*") {
    Write-Host "==> Adding $InstallDir to system PATH"
    [Environment]::SetEnvironmentVariable("Path", "$machinePath;$InstallDir", "Machine")
}

Write-Host ""
Write-Host "==> Installation complete!" -ForegroundColor Green
Write-Host ""
Write-Host "    1. Edit config:  notepad `"$ConfigPath`""
Write-Host "    2. Start:        Start-Service $ServiceName"
Write-Host "    3. Status:       Get-Service $ServiceName"
Write-Host "    4. Logs:         Get-EventLog -LogName Application -Source $ServiceName"
Write-Host ""
Write-Host "    Admin API:       http://localhost:9090/api/clients"
Write-Host ""
