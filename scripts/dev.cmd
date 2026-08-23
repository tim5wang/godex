@echo off
rem scripts/dev.cmd - Windows equivalent of `make dev`
rem
rem Full rebuild + service restart (with tsc type-check + vite bundle):
rem   1. pnpm --dir ui/web build          (tsc -b && vite build)
rem   2. go build with version ldflags -> godex.exe.new
rem   3. stop service, publish binary, restart service
rem
rem Usage:
rem   scripts\dev.cmd
rem   scripts\dev.cmd --skip-web          (skip web build for a pure Go rebuild)
rem
rem Note: on Windows the running godex.exe is file-locked, so unlike the
rem Makefile's atomic `mv`, we must stop the service before replacing it.
rem Set VERSION to override, e.g. `set VERSION=v1.5.0 && scripts\dev.cmd`.

setlocal EnableDelayedExpansion
set "APP=godex"
if not defined VERSION set "VERSION=v1.4.0"

rem ── Locate repo root (this file lives in scripts/) ────────────────
cd /d "%~dp0.."

rem ── Capture commit + build date for ldflags ───────────────────────
for /f "delims=" %%i in ('git rev-parse --short=12 HEAD 2^>nul') do set "COMMIT=%%i"
if not defined COMMIT set "COMMIT=unknown"
for /f "delims=" %%i in ('powershell -NoProfile -Command "Get-Date -Date ([DateTime]::UtcNow) -Format o"') do set "BUILD_DATE=%%i"

set "LDFLAGS=-s -w -X github.com/tim5wang/godex/internal/version.Version=%VERSION% -X github.com/tim5wang/godex/internal/version.Commit=%COMMIT% -X github.com/tim5wang/godex/internal/version.Date=%BUILD_DATE%"

echo [dev] version=%VERSION% commit=%COMMIT% built=%BUILD_DATE%

rem ── 1. Web UI production build (tsc + vite) ───────────────────────
if /i "%~1"=="--skip-web" (
  echo [dev] skipping web build
) else (
  echo [dev] building web UI (tsc + vite)...
  call pnpm --dir ui\web build
  if errorlevel 1 (
    echo [dev] ERROR: web build failed
    exit /b 1
  )
)

rem ── 2. Go build → godex.exe.new ───────────────────────────────────
echo [dev] building %APP%.exe.new...
go build -ldflags "%LDFLAGS%" -o "%APP%.exe.new" .\cmd\godex
if errorlevel 1 (
  echo [dev] ERROR: go build failed
  exit /b 1
)

rem ── 3. Stop service, publish binary, restart ──────────────────────
echo [dev] stopping service (unlock running binary)...
call "%APP%.exe" service stop >nul 2>&1

set /a tries=0
:retry_move
set /a tries+=1
move /Y "%APP%.exe.new" "%APP%.exe" >nul 2>&1
if errorlevel 1 (
  if !tries! GEQ 10 (
    echo [dev] ERROR: could not replace %APP%.exe - is the service still running?
    exit /b 1
  )
  echo [dev]   binary still locked, retrying (!tries!/10)...
  timeout /t 1 /nobreak >nul
  goto retry_move
)
echo [dev] published %APP%.exe

echo [dev] restarting service...
call "%APP%.exe" service restart >nul 2>&1
if errorlevel 1 (
  echo [dev]   restart failed, trying install + start...
  call "%APP%.exe" service install >nul 2>&1
  call "%APP%.exe" service start
  if errorlevel 1 (
    echo [dev] ERROR: service install/start failed
    exit /b 1
  )
)

echo [dev] done
endlocal
