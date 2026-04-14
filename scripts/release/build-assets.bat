@echo off
setlocal enabledelayedexpansion

REM ============================================================
REM  Brain release builder for Windows
REM  Usage:  build-assets.bat <version> [output-dir]
REM  Example: build-assets.bat 0.6.0
REM           build-assets.bat 0.6.0 dist
REM ============================================================

if "%~1"=="" (
    set /p "version=Enter version (e.g. 0.6.0): "
    if "!version!"=="" (
        echo ERROR: version is required.
        pause
        exit /b 64
    )
) else (
    set "version=%~1"
)
REM strip leading 'v' if present
if "%version:~0,1%"=="v" set "version=%version:~1%"

set "outdir=%~2"
if "%outdir%"=="" set "outdir=dist"

set "script_dir=%~dp0"
set "root_dir=%script_dir%..\.."

REM resolve to absolute path
pushd "%root_dir%"
set "root_dir=%CD%"
popd

REM --- build metadata ---
for /f "delims=" %%c in ('git -C "%root_dir%" rev-parse --short=12 HEAD 2^>nul') do set "build_commit=%%c"
if not defined build_commit set "build_commit=unknown"

for /f "delims=" %%t in ('powershell -nologo -command "Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ' -AsUTC" 2^>nul') do set "build_time=%%t"
if not defined build_time set "build_time=unknown"

set "ldflags=-s -w -X github.com/leef-l/brain.BuildCommit=%build_commit% -X github.com/leef-l/brain.BuildTime=%build_time%"

REM --- output directory ---
if not exist "%outdir%" mkdir "%outdir%"
pushd "%outdir%"
set "outdir=%CD%"
popd

REM --- collect binaries to build ---
set "bin_count=0"

REM fixed binaries
call :add_binary "brain" ".\cmd\brain"
call :add_binary "brain-central" ".\central\cmd"

REM Pattern 1: brains\<name>\cmd\main.go -> brain-<name>
for /d %%d in ("%root_dir%\brains\*") do (
    if exist "%%d\cmd\main.go" (
        call :add_binary "brain-%%~nxd" ".\brains\%%~nxd\cmd"
    )
)

REM Pattern 2: brains\<parent>\<sub>\cmd\main.go -> brain-<parent>-<sub>
for /d %%p in ("%root_dir%\brains\*") do (
    for /d %%s in ("%%p\*") do (
        if exist "%%s\cmd\main.go" (
            REM skip if parent already matched by pattern 1
            if not exist "%%p\cmd\main.go" (
                call :add_binary "brain-%%~nxp-%%~nxs" ".\brains\%%~nxp\%%~nxs\cmd"
            )
        )
    )
)

echo.
echo ========================================
echo  Building %bin_count% binaries (windows/amd64)
echo ========================================
echo.

REM --- build each binary ---
set "errors=0"
for /l %%i in (0,1,%bin_count%) do (
    if defined bin_name_%%i (
        set "bname=!bin_name_%%i!"
        set "bpkg=!bin_pkg_%%i!"
        echo   [%%i/%bin_count%] !bname!  ^(!bpkg!^)
        set "CGO_ENABLED=0"
        set "GOOS=windows"
        set "GOARCH=amd64"
        go build -trimpath -ldflags "%ldflags%" -o "%outdir%\!bname!.exe" "!bpkg!"
        if errorlevel 1 (
            echo   ERROR: failed to build !bname! >&2
            set /a "errors+=1"
        )
    )
)

REM --- copy metadata files ---
echo.
echo Copying metadata files...
for %%f in (VERSION.json LICENSE README.md CHANGELOG.md SECURITY.md) do (
    if exist "%root_dir%\%%f" (
        copy /y "%root_dir%\%%f" "%outdir%\" >nul
    )
)

REM --- generate SHA256 checksums ---
echo Generating checksums...
pushd "%outdir%"
if exist SHA256SUMS del SHA256SUMS
powershell -nologo -command ^
    "Get-ChildItem -File | ForEach-Object { $h = (Get-FileHash $_.Name -Algorithm SHA256).Hash.ToLower(); \"$h  $($_.Name)\" } | Sort-Object | Set-Content SHA256SUMS -Encoding UTF8"
popd

echo.
echo ========================================
if %errors% equ 0 (
    echo  Done! %bin_count% binaries built to %outdir%\
) else (
    echo  Done with %errors% error(s). Check output above.
)
echo ========================================
echo.

dir /b "%outdir%"

REM keep window open when double-clicked
if "%~1"=="" pause
exit /b %errors%

REM ============================================================
REM  Helper: add a binary to the build list
REM ============================================================
:add_binary
set "bin_name_%bin_count%=%~1"
set "bin_pkg_%bin_count%=%~2"
set /a "bin_count+=1"
exit /b 0
