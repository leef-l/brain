@echo off
setlocal enabledelayedexpansion

REM ============================================================
REM  Brain release builder for Windows
REM  Usage:  build-assets.bat [version] [output-dir]
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
if "!version:~0,1!"=="v" set "version=!version:~1!"

set "script_dir=%~dp0"
set "root_dir=%script_dir%..\.."

pushd "%root_dir%"
set "root_dir=%CD%"
popd

set "outdir=%~2"
if "%outdir%"=="" set "outdir=%root_dir%\dist"

for /f "delims=" %%c in ('git -C "%root_dir%" rev-parse --short=12 HEAD 2^>nul') do set "build_commit=%%c"
if not defined build_commit set "build_commit=unknown"

for /f "delims=" %%t in ('powershell -nologo -command "Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ' -AsUTC" 2^>nul') do set "build_time=%%t"
if not defined build_time set "build_time=unknown"

set "ldflags=-s -w -X github.com/leef-l/brain.CLIVersion=!version! -X github.com/leef-l/brain.SDKVersion=!version! -X github.com/leef-l/brain.KernelVersion=!version! -X github.com/leef-l/brain.BuildCommit=!build_commit! -X github.com/leef-l/brain.BuildTime=!build_time!"

if not exist "%outdir%" mkdir "%outdir%"
pushd "%outdir%"
set "outdir=%CD%"
popd

set "bin_count=0"
set "errors=0"

pushd "%root_dir%"

echo Cleaning Go build cache...
go clean -cache >nul 2>&1

echo.
echo ========================================
echo  Building binaries (windows/amd64)
echo  Output:  !outdir!
echo ========================================
echo.

call :build_one "brain" ".\cmd\brain"
call :build_one "brain-central" ".\central\cmd"

REM Pattern 1: brains [name] cmd main.go
for /d %%d in ("%root_dir%\brains\*") do (
    set "_name=%%~nxd"
    if exist "%%d\cmd\main.go" (
        if exist "%%d\cmd\brain-!_name!-sidecar\main.go" (
            call :build_one "brain-!_name!" ".\brains\!_name!\cmd"
        ) else (
            call :build_one "brain-!_name!-sidecar" ".\brains\!_name!\cmd"
        )
    )
)

REM Pattern 2: brains [parent] [sub] cmd main.go
for /d %%p in ("%root_dir%\brains\*") do (
    set "_parent=%%~nxp"
    for /d %%s in ("%%p\*") do (
        set "_sub=%%~nxs"
        if exist "%%s\cmd\main.go" (
            if not exist "%%p\cmd\main.go" (
                call :build_one "brain-!_parent!-!_sub!" ".\brains\!_parent!\!_sub!\cmd"
            )
        )
    )
)

REM Pattern 3: brains [name] cmd brain-[name]-sidecar main.go
for /d %%d in ("%root_dir%\brains\*") do (
    set "_name=%%~nxd"
    if exist "%%d\cmd\brain-!_name!-sidecar\main.go" (
        call :build_one "brain-!_name!-sidecar" ".\brains\!_name!\cmd\brain-!_name!-sidecar"
    )
)

popd

set "gobin="
for /f "delims=" %%g in ('go env GOPATH 2^>nul') do set "gobin=%%g\bin"
if not "%GOBIN%"=="" set "gobin=%GOBIN%"
if not "%gobin%"=="" (
    if not exist "%gobin%" mkdir "%gobin%"
    echo.
    echo Installing to !gobin! ...
    for %%f in ("!outdir!\*.exe") do (
        copy /y "%%f" "!gobin!\" >nul
        echo   to !gobin!\%%~nxf
    )
)

echo.
echo Copying metadata files...
for %%f in (VERSION.json LICENSE README.md CHANGELOG.md SECURITY.md) do (
    if exist "%root_dir%\%%f" (
        copy /y "%root_dir%\%%f" "!outdir!\" >nul
    )
)

echo Generating checksums...
pushd "!outdir!"
if exist SHA256SUMS del SHA256SUMS
powershell -nologo -command ^
    "Get-ChildItem -File | ForEach-Object { $h = (Get-FileHash $_.Name -Algorithm SHA256).Hash.ToLower(); \"$h  $($_.Name)\" } | Sort-Object | Set-Content SHA256SUMS -Encoding UTF8"
popd

echo.
echo ========================================
if !errors! equ 0 (
    echo  Done! !bin_count! binaries built to !outdir!\
) else (
    echo  Done with !errors! error(s). Check output above.
)
echo ========================================
echo.

dir /b "!outdir!"

if "%~1"=="" (
    pause
) else (
    if not !errors! equ 0 (
        echo.
        echo *** BUILD FAILED with !errors! error.s. press any key to exit ***
        pause
    )
)
exit /b !errors!

:build_one
set /a "bin_count+=1"
set "_bname=%~1"
set "_bpkg=%~2"
echo   [!bin_count!] !_bname!  ^(!_bpkg!^)
set "CGO_ENABLED=0"
set "GOOS=windows"
set "GOARCH=amd64"
go build -trimpath -ldflags "%ldflags%" -o "!outdir!\!_bname!.exe" "!_bpkg!"
if errorlevel 1 (
    echo   ERROR: failed to build !_bname! >&2
    set /a "errors+=1"
)
exit /b 0
