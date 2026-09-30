#!/usr/bin/env bash
# 一键构建并部署 Android：Web UI → godex 交叉编译 → APK → adb 安装。
#
# 关键点：godex serve 的 Web UI 通过 go:embed 内嵌在 libgodex.so 里。
# 普通 assembleDebug 只检查 native 产物；本脚本显式准备 runtime 并重编 libgodex.so，
# 确保 APK 包含当前 Web UI。
#
# 用法:
#   bash mobile/scripts/build-and-install-android.sh            # 全流程 + 安装
#   bash mobile/scripts/build-and-install-android.sh --skip-web # 跳过 Web UI 构建
#   bash mobile/scripts/build-and-install-android.sh --no-install  # 只构建不安装
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
SKIP_WEB=0
NO_INSTALL=0
for arg in "$@"; do
  case "$arg" in
    --skip-web) SKIP_WEB=1 ;;
    --no-install) NO_INSTALL=1 ;;
    *) echo "未知参数: $arg（支持 --skip-web / --no-install）" >&2; exit 1 ;;
  esac
done

# ---- 环境 ----
export PATH="/usr/local/go/bin:/usr/local/bin:$PATH"
export JAVA_HOME="${JAVA_HOME:-$HOME/Library/Java/JavaVirtualMachines/jdk-21.0.12.1+1/Contents/Home}"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"

command -v go >/dev/null || { echo "缺少 go，请安装 Go ≥ 1.22"; exit 1; }
[ -d "$JAVA_HOME" ] || { echo "缺少 JAVA_HOME: $JAVA_HOME"; exit 1; }
[ -d "$ANDROID_HOME" ] || { echo "缺少 ANDROID_HOME: $ANDROID_HOME"; exit 1; }

# ---- 1) Web UI（输出到 internal/uiassets/embedded_dist）----
if [ "$SKIP_WEB" = "0" ]; then
  echo "==> [1/4] 构建 Web UI (vite build → embedded_dist)"
  (cd "$ROOT/ui/web" && corepack pnpm exec vite build)
else
  echo "==> [1/4] 跳过 Web UI 构建（--skip-web）"
fi

# ---- 2) 显式准备 runtime 并交叉编译最新 godex UI ----
echo "==> [2/4] 准备 Android native runtime 并构建 libgodex.so"
(cd "$ROOT/mobile/android" && ./gradlew prepareGodexNativeRuntime --no-daemon -PforceGodexBuild)

# ---- 3) APK ----
echo "==> [3/4] 构建 APK (gradle assembleDebug)"
(cd "$ROOT/mobile/android" && ./gradlew assembleDebug --no-daemon)

APK="$ROOT/mobile/android/app/build/outputs/apk/debug/app-debug.apk"
[ -f "$APK" ] || { echo "APK 未生成: $APK"; exit 1; }
echo "==> APK 就绪: $APK"
ls -lh "$APK" | awk '{print "    size:", $5, "time:", $6, $7, $8}'

# ---- 4) adb 安装 ----
if [ "$NO_INSTALL" = "0" ]; then
  ADB="$ANDROID_HOME/platform-tools/adb"
  "$ADB" devices
  echo "==> [4/4] adb install -r（请在手机弹出的安装确认中点「继续安装」）"
  "$ADB" install -r "$APK"
  echo "==> 完成，已安装到设备"
else
  echo "==> [4/4] 跳过安装（--no-install），APK 已就绪"
fi
