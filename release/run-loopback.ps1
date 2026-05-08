# reversproxy — loopback test runner
#
# 한 대의 Windows PC에서 server + client + 더미 HTTP 백엔드를 동시에 띄우고,
# 대시보드를 브라우저로 엽니다.
#
# 사용:
#   irm https://raw.githubusercontent.com/EcoKG/reversproxy/release/loopback-test/release/run-loopback.ps1 | iex
#
# 또는 폴더에 직접 풀어서:
#   .\run-loopback.ps1

$ErrorActionPreference = 'Stop'

$RepoRaw = 'https://raw.githubusercontent.com/EcoKG/reversproxy/release/loopback-test/release'
$WorkDir = Join-Path $env:TEMP 'reversproxy-loopback'

Write-Host "[*] 작업 디렉토리: $WorkDir" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $WorkDir | Out-Null
Set-Location $WorkDir

# ---------------------------------------------------------------------------
# 1) 바이너리 + 설정 다운로드
# ---------------------------------------------------------------------------
$Files = @(
  'reversproxy-server.exe',
  'reversproxy-client.exe',
  'server.yaml',
  'client.yaml'
)

foreach ($f in $Files) {
  $dest = Join-Path $WorkDir $f
  if (Test-Path $dest) { Remove-Item $dest -Force }
  Write-Host "[*] 다운로드: $f" -ForegroundColor Cyan
  Invoke-WebRequest -Uri "$RepoRaw/$f" -OutFile $dest -UseBasicParsing
}

# ---------------------------------------------------------------------------
# 2) 더미 HTTP 백엔드 (PowerShell 내장 HttpListener) — 포트 3000
# ---------------------------------------------------------------------------
Write-Host "[*] 더미 HTTP 백엔드 시작 (http://127.0.0.1:3000)" -ForegroundColor Cyan
$BackendJob = Start-Job -Name 'rp-loopback-backend' -ScriptBlock {
  $listener = New-Object System.Net.HttpListener
  $listener.Prefixes.Add('http://127.0.0.1:3000/')
  $listener.Start()
  while ($listener.IsListening) {
    try {
      $ctx = $listener.GetContext()
      $body = "<!doctype html><html><body><h1>reversproxy loopback OK</h1>" +
              "<p>backend serving at 127.0.0.1:3000</p>" +
              "<p>request path: $($ctx.Request.Url.AbsolutePath)</p></body></html>"
      $bytes = [Text.Encoding]::UTF8.GetBytes($body)
      $ctx.Response.ContentType = 'text/html; charset=utf-8'
      $ctx.Response.OutputStream.Write($bytes, 0, $bytes.Length)
      $ctx.Response.OutputStream.Close()
    } catch { break }
  }
}

# ---------------------------------------------------------------------------
# 3) 클라이언트 시작 (먼저 listen 시작해야 서버가 dial 성공)
# ---------------------------------------------------------------------------
Write-Host "[*] reversproxy-client 시작 (127.0.0.1:8443 listen)" -ForegroundColor Cyan
$ClientProc = Start-Process -FilePath ".\reversproxy-client.exe" `
  -ArgumentList @('--config', '.\client.yaml') `
  -PassThru -NoNewWindow `
  -RedirectStandardOutput 'client.log' -RedirectStandardError 'client.err.log'

Start-Sleep -Seconds 1

# ---------------------------------------------------------------------------
# 4) 서버 시작
# ---------------------------------------------------------------------------
Write-Host "[*] reversproxy-server 시작 (admin: 127.0.0.1:9090)" -ForegroundColor Cyan
$ServerProc = Start-Process -FilePath ".\reversproxy-server.exe" `
  -ArgumentList @('--config', '.\server.yaml') `
  -PassThru -NoNewWindow `
  -RedirectStandardOutput 'server.log' -RedirectStandardError 'server.err.log'

Start-Sleep -Seconds 2

# ---------------------------------------------------------------------------
# 5) 검증
# ---------------------------------------------------------------------------
Write-Host ""
Write-Host "============================================================" -ForegroundColor Green
Write-Host " 실행 중. 다음 URL 들을 브라우저/cURL 로 확인하세요:" -ForegroundColor Green
Write-Host "============================================================" -ForegroundColor Green
Write-Host ""
Write-Host "  대시보드     :  http://127.0.0.1:9090/"            -ForegroundColor Yellow
Write-Host "  TCP 터널     :  http://127.0.0.1:19000/"           -ForegroundColor Yellow
Write-Host "  HTTP 라우팅  :  curl -H 'Host: test.local' http://127.0.0.1:18080/" -ForegroundColor Yellow
Write-Host "  백엔드 직접  :  http://127.0.0.1:3000/"            -ForegroundColor Yellow
Write-Host "  REST API     :  http://127.0.0.1:9090/api/clients" -ForegroundColor Yellow
Write-Host ""
Write-Host "  로그 파일    :  $WorkDir\\server.log, client.log"  -ForegroundColor DarkGray
Write-Host "  중지         :  이 PowerShell 창에서 Ctrl+C"         -ForegroundColor DarkGray
Write-Host ""

# 자동 검증
try {
  Start-Sleep -Seconds 1
  $r = Invoke-WebRequest -Uri 'http://127.0.0.1:19000/' -UseBasicParsing -TimeoutSec 5
  if ($r.StatusCode -eq 200) {
    Write-Host "[OK] TCP 터널 검증 성공 (HTTP 200, $($r.Content.Length) bytes)" -ForegroundColor Green
  }
} catch {
  Write-Host "[!] TCP 터널 자동 검증 실패: $_" -ForegroundColor Yellow
}

# 대시보드 자동 오픈
Start-Process 'http://127.0.0.1:9090/'

# ---------------------------------------------------------------------------
# 6) Ctrl+C 처리
# ---------------------------------------------------------------------------
try {
  Wait-Process -Id $ServerProc.Id, $ClientProc.Id -ErrorAction SilentlyContinue
} finally {
  Write-Host ""
  Write-Host "[*] 정리 중..." -ForegroundColor Cyan
  Stop-Process -Id $ServerProc.Id -Force -ErrorAction SilentlyContinue
  Stop-Process -Id $ClientProc.Id -Force -ErrorAction SilentlyContinue
  Stop-Job -Name 'rp-loopback-backend' -ErrorAction SilentlyContinue
  Remove-Job -Name 'rp-loopback-backend' -ErrorAction SilentlyContinue
  Write-Host "[*] 종료 완료" -ForegroundColor Cyan
}
