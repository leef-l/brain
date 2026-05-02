@echo off
setlocal enabledelayedexpansion

REM ============================================================
REM  Brain release builder for Windows
REM  Usage:  build-assets.bat <version> [output-dir]
REM  Example: build-assets.bat 0.6.0
REM           build-assets.bat 0.6.0 dist
REM ============================================================
REM
REM 修复历史:旧版用 bin_name_0/bin_pkg_0 间接变量 +
REM   for /l %%i in (0,1,%bin_count%) 跑构建。在 enabledelayedexpansion +
REM   括号代码块 + for 修饰符(%%~nxd)组合下,!bin_count! 在 for /l 解析
REM   行就被定型为初始 0,导致后续 add_binary 累积无效,只编出第一个二进制。
REM
REM 新版策略:add_binary 不再累积间接变量,而是当场调 go build。
REM   构建顺序与 add_binary 调用顺序一致,bin_count 仅用于显示编号。
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
if "!version:~0,1!"=="v" set "version=!version:~1!"

set "script_dir=%~dp0"
set "root_dir=%script_dir%..\.."

REM resolve to absolute path
pushd "%root_dir%"
set "root_dir=%CD%"
popd

REM output dir always relative to project root
set "outdir=%~2"
if "%outdir%"=="" set "outdir=%root_dir%\dist"

REM --- build metadata ---
for /f "delims=" %%c in ('git -C "%root_dir%" rev-parse --short=12 HEAD 2^>nul') do set "build_commit=%%c"
if not defined build_commit set "build_commit=unknown"

for /f "delims=" %%t in ('powershell -nologo -command "Get-Date -Format 'yyyy-MM-ddTHH:mm:ssZ' -AsUTC" 2^>nul') do set "build_time=%%t"
if not defined build_time set "build_time=unknown"

set "ldflags=-s -w -X github.com/leef-l/brain.CLIVersion=!version! -X github.com/leef-l/brain.SDKVersion=!version! -X github.com/leef-l/brain.KernelVersion=!version! -X github.com/leef-l/brain.BuildCommit=!build_commit! -X github.com/leef-l/brain.BuildTime=!build_time!"

REM --- output directory ---
if not exist "%outdir%" mkdir "%outdir%"
pushd "%outdir%"
set "outdir=%CD%"
popd

REM --- build counters ---
set "bin_count=0"
set "errors=0"

REM 切到项目根,让 .\cmd\brain 等相对路径生效
pushd "%root_dir%"

REM --- 强制清编译缓存,避免打出旧代码 ---
REM 必要原因:
REM 1) //go:embed 资源不参与默认 build cache key,改前端 static 后不清缓存
REM    会拿到旧资源(全局 CLAUDE.md feedback_embed_cache 铁律已记录)。
REM 2) -ldflags "-X ...BuildCommit=xxx" 的值不参与 cache key,如果同 commit
REM    之前编过别的版本(切 branch / cherry-pick),这次会命中缓存输出旧二进制。
REM 3) -trimpath 让不同 worktree 的同源码 hash 共享缓存,跨 worktree 串扰。
REM 注:仅清 build cache,不清 modcache(后者要重新下依赖,过慢)
echo Cleaning Go build cache (not modcache)...
go clean -cache >nul 2>&1

echo.
echo ========================================
echo  Building binaries (windows/amd64)
echo  Output:  !outdir!
echo ========================================
echo.

REM fixed binaries
call :build_one "brain" ".\cmd\brain"
call :build_one "brain-central" ".\central\cmd"

REM Pattern 1: brains\<name>\cmd\main.go
REM   若同目录存在 brain-<name>-sidecar\ 子目录(data/quant 这种双模式),
REM     cmd\main.go 是独立运行入口 -> brain-<name>(不带 sidecar)
REM   否则 cmd\main.go 就是 sidecar 入口 -> brain-<name>-sidecar
REM     与 brain.json manifest 的 entrypoint 字段一致,便于 Kernel 定位。
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

REM Pattern 2: brains\<parent>\<sub>\cmd\main.go -> brain-<parent>-<sub>
for /d %%p in ("%root_dir%\brains\*") do (
    set "_parent=%%~nxp"
    for /d %%s in ("%%p\*") do (
        set "_sub=%%~nxs"
        if exist "%%s\cmd\main.go" (
            REM skip if parent already matched by pattern 1
            if not exist "%%p\cmd\main.go" (
                call :build_one "brain-!_parent!-!_sub!" ".\brains\!_parent!\!_sub!\cmd"
            )
        )
    )
)

REM Pattern 3: brains\<name>\cmd\brain-<name>-sidecar\main.go -> brain-<name>-sidecar
REM 双模式大脑(data/quant):cmd\main.go 是独立入口,cmd\brain-<name>-sidecar\main.go
REM 才是 Kernel 通过 stdio JSON-RPC 启动的 sidecar 入口。
REM 无 cmd\main.go 的 brain(easymvp)也只命中本 Pattern。
for /d %%d in ("%root_dir%\brains\*") do (
    set "_name=%%~nxd"
    if exist "%%d\cmd\brain-!_name!-sidecar\main.go" (
        call :build_one "brain-!_name!-sidecar" ".\brains\!_name!\cmd\brain-!_name!-sidecar"
    )
)

popd

REM --- install to GOPATH\bin (覆盖式) ---
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

REM --- copy metadata files ---
echo.
echo Copying metadata files...
for %%f in (VERSION.json LICENSE README.md CHANGELOG.md SECURITY.md) do (
    if exist "%root_dir%\%%f" (
        copy /y "%root_dir%\%%f" "!outdir!\" >nul
    )
)

REM --- generate SHA256 checksums ---
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

REM 窗口保留策略:
REM - 双击运行(无参数)总是 pause(老规则保留)
REM - 带参数运行时,有错误必 pause 让用户看清问题;成功无错误则正常退出
if "%~1"=="" (
    pause
) else (
    if not !errors! equ 0 (
        echo.
        echo *** BUILD FAILED with !errors! error(s),请阅读上方红色 ERROR 行后按任意键退出 ***
        pause
    )
)
exit /b !errors!

REM ============================================================
REM  Helper: build one binary
REM  在 :label 子例程内调 go build。子例程是独立的命令解析上下文,
REM  延迟展开 + 间接变量的求值时机问题不会出现。
REM ============================================================
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
