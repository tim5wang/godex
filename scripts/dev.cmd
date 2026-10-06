@echo off
rem scripts/dev.cmd - Windows equivalent of `make dev`
rem
rem Full rebuild + service restart (with tsc type-check + vite bundle):
rem   0. pnpm install in ui/web              (only when node_modules is missing)
rem   1. pnpm build in ui/web                (tsc -b && vite build)
rem   2. go build with version ldflags -> godex.exe.new
rem   3. stop service, publish binary, restart service
rem
rem Usage:
rem   scripts\dev.cmd
rem   scripts\dev.cmd --skip-web          (skip web build for a pure Go rebuild)
rem   scripts\dev.cmd --skip-install      (skip the web dependency install check)
rem   scripts\dev.cmd --skip-service      (build + publish only; do not touch the service)
rem
rem Notes:
rem   - pnpm is resolved through `corepack` when it is available, and the pnpm
rem     commands run inside ui/web so the repo's `packageManager`
rem     (pnpm@10.11.0) is honored. Running corepack from the repo root makes it
rem     try to fetch "pnpm/latest" over the network, and a global pnpm 9 cannot
rem     read a settings-only pnpm-workspace.yaml ("packages field missing or
rem     empty").
rem   - on Windows the running godex.exe is file-locked, so unlike the Makefile's
rem     atomic `mv`, we must stop the service before replacing it.
rem   - set VERSION to override, e.g. `set VERSION=v1.5.0 && scripts\dev.cmd`.
rem   - service failures are warnings by default (the binary is already built and
rem     published); set GODEX_DEV_STRICT_SERVICE=1 to make them fatal.
rem
rem Implementation note: this script deliberately uses goto-based flow instead
rem of large parenthesised if/else blocks, because an unescaped ")" inside an
rem echoed message would close the block early and silently break flag handling.

setlocal EnableDelayedExpansion
set "APP=godex"
if not defined VERSION set "VERSION=v1.4.0"

rem -- Locate repo root (this file lives in scripts/) --
cd /d "%~dp0.."

rem -- Parse flags (order independent) --
set "SKIP_WEB="
set "SKIP_INSTALL="
set "SKIP_SERVICE="
:parse_args
if "%~1"=="" goto args_done
if /i "%~1"=="--skip-web"     set "SKIP_WEB=1"
if /i "%~1"=="--skip-install" set "SKIP_INSTALL=1"
if /i "%~1"=="--skip-service" set "SKIP_SERVICE=1"
shift
goto parse_args
:args_done

rem -- Resolve pnpm: prefer corepack so ui/web packageManager (pnpm@10.11.0) wins --
set "PNPM=pnpm"
where corepack >nul 2>&1 && set "PNPM=corepack pnpm"
rem Never let corepack reach out for "latest"; ui/web pins the exact version.
set "COREPACK_DEFAULT_TO_LATEST=0"

rem -- Capture commit + build date for ldflags --
for /f "delims=" %%i in ('git rev-parse --short=12 HEAD 2^>nul') do set "COMMIT=%%i"
if not defined COMMIT set "COMMIT=unknown"
set "BUILD_DATE="
for /f "delims=" %%i in ('powershell -NoProfile -Command "Get-Date -Date ([DateTime]::UtcNow) -Format o" 2^>nul') do set "BUILD_DATE=%%i"
if not defined BUILD_DATE set "BUILD_DATE=%DATE% %TIME%"

set "LDFLAGS=-s -w -X github.com/tim5wang/godex/internal/version.Version=%VERSION% -X github.com/tim5wang/godex/internal/version.Commit=%COMMIT% -X github.com/tim5wang/godex/internal/version.Date=%BUILD_DATE%"

echo [dev] version=%VERSION% commit=%COMMIT% built=%BUILD_DATE%
echo [dev] pnpm launcher: %PNPM%

rem -- 0. Ensure web dependencies are installed --
if defined SKIP_INSTALL goto after_install
if exist ui\web\node_modules\.bin goto after_install
echo [dev] installing web dependencies
pushd ui\web
call %PNPM% install
set "INSTALL_RC=%errorlevel%"
if "%INSTALL_RC%"=="0" goto install_ok
echo [dev] WARN: install with lifecycle scripts failed, retrying with --ignore-scripts
call %PNPM% install --ignore-scripts
set "INSTALL_RC=%errorlevel%"
if not "%INSTALL_RC%"=="0" goto install_failed
:install_ok
popd
goto after_install
:install_failed
popd
echo [dev] ERROR: web dependency install failed
exit /b 1
:after_install

rem -- 1. Web UI production build (tsc + vite) --
if defined SKIP_WEB goto web_skipped
rem Clean the previous bundle up front. vite's emptyOutDir does the same, but a
rem single bulk delete of the whole assets dir can trip restrictive file guards
rem on some machines; removing it here is equivalent and more robust.
if exist internal\uiassets\embedded_dist rmdir /s /q internal\uiassets\embedded_dist
echo [dev] building web UI - tsc + vite
pushd ui\web
call %PNPM% build
set "WEB_RC=%errorlevel%"
popd
if not "%WEB_RC%"=="0" goto web_failed
goto after_web
:web_skipped
echo [dev] skipping web build
goto after_web
:web_failed
echo [dev] ERROR: web build failed
exit /b 1
:after_web

rem -- 2. Go build -- godex.exe.new --
echo [dev] building %APP%.exe.new
go build -ldflags "%LDFLAGS%" -o "%APP%.exe.new" .\cmd\godex
if errorlevel 1 goto go_failed
goto after_go
:go_failed
echo [dev] ERROR: go build failed
exit /b 1
:after_go

rem -- 3. Stop service, publish binary, restart --
if defined SKIP_SERVICE goto service_skip_stop
echo [dev] stopping service - unlock running binary
call "%APP%.exe" service stop >nul 2>&1
goto publish_done
:service_skip_stop
echo [dev] skipping service stop
:publish_done

set /a tries=0
:retry_move
set /a tries+=1
move /Y "%APP%.exe.new" "%APP%.exe" >nul 2>&1
if not errorlevel 1 goto move_ok
if !tries! GEQ 10 goto move_failed
echo [dev]   binary still locked, retrying !tries!/10
timeout /t 1 /nobreak >nul
goto retry_move
:move_failed
echo [dev] ERROR: could not replace %APP%.exe - is the service still running?
exit /b 1
:move_ok
echo [dev] published %APP%.exe

if defined SKIP_SERVICE goto service_skipped_restart

echo [dev] restarting service
call "%APP%.exe" service restart >nul 2>&1
if not errorlevel 1 goto service_ok
echo [dev]   restart failed, trying install + start
call "%APP%.exe" service install >nul 2>&1
call "%APP%.exe" service start >nul 2>&1
if not errorlevel 1 goto service_ok
if defined GODEX_DEV_STRICT_SERVICE goto service_failed
echo [dev] WARN: could not restart the godex service - the binary was still built and published.
echo [dev] WARN: start it manually: %APP%.exe service install  then  %APP%.exe service start
echo [dev] WARN: set GODEX_DEV_STRICT_SERVICE=1 to make this fatal.
goto done
:service_failed
echo [dev] ERROR: service install/start failed
exit /b 1
:service_ok
goto done

:service_skipped_restart
echo [dev] skipping service restart

:done
echo [dev] done
endlocal
