@echo off
echo ========================================
echo  CloseProxy Build
echo ========================================
echo.

set CSC=C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe

echo [1/2] Building server.exe (for 221 - air-gapped)...
%CSC% /nologo /optimize /out:server.exe server.cs
if %errorlevel% neq 0 (
    echo FAILED to build server.exe
    pause
    exit /b 1
)
echo       server.exe OK

echo [2/2] Building client.exe (for 224 - internet)...
%CSC% /nologo /optimize /out:client.exe client.cs
if %errorlevel% neq 0 (
    echo FAILED to build client.exe
    pause
    exit /b 1
)
echo       client.exe OK

echo.
echo ========================================
echo  Build complete!
echo ========================================
echo.
echo  1. server.exe -^> 192.168.1.221 에 복사해서 실행
echo  2. client.exe -^> 192.168.4.224 에서 실행
echo.
echo  [221에서]
echo    server.exe
echo    set HTTPS_PROXY=http://127.0.0.1:8080
echo    claude
echo.
echo  [224에서]
echo    client.exe 192.168.1.221 9000
echo.
pause
