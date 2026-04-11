@echo off
REM Build GoAMPP control panel as a 64-bit Windows GUI app (no console window).
REM -H windowsgui   hide console
REM -s -w           strip debug/symbol tables for a smaller binary
setlocal
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0

echo Building goampp.exe...
go build -ldflags="-H windowsgui -s -w" -trimpath -o goampp.exe .
if errorlevel 1 (
    echo Build FAILED.
    exit /b 1
)

echo.
echo Build OK:
dir goampp.exe | findstr goampp.exe
echo.
echo Optional: run `upx --best goampp.exe` for further compression.
endlocal
