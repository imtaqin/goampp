@echo off
REM Build goampp.exe, then the Inno Setup installer into dist\.
REM Usage: build-installer.bat [version]   (default 0.0.0)
setlocal
set VERSION=%~1
if "%VERSION%"=="" set VERSION=0.0.0

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0

echo Building goampp.exe v%VERSION%...
go build -ldflags="-H windowsgui -s -w -X main.appVersion=v%VERSION%" -trimpath -o goampp.exe .
if errorlevel 1 (
    echo Build FAILED.
    exit /b 1
)

REM iscc on PATH, else a machine-wide or per-user (winget) Inno Setup 6.
set ISCC=iscc
where iscc >nul 2>nul && goto :haveiscc
set ISCC="%ProgramFiles(x86)%\Inno Setup 6\ISCC.exe"
if exist %ISCC% goto :haveiscc
set ISCC="%LOCALAPPDATA%\Programs\Inno Setup 6\ISCC.exe"
:haveiscc

echo Building installer...
%ISCC% /Q /DAppVersion=%VERSION% installer\goampp.iss
if errorlevel 1 (
    echo Installer build FAILED.
    exit /b 1
)

echo.
echo Installer OK:
dir dist\goampp-setup-%VERSION%.exe | findstr goampp-setup
endlocal
