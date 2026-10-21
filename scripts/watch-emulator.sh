#!/usr/bin/env bash
# 手表本地仿真环境（Android Emulator / Wear OS）：创建、启停、门禁验证。
#
# 为什么需要：真机无线调试被企业网络阻断（LAN TCP 被套接字过滤扩展拦截，见
# docs/tools_issues.md 2026-10-21 记录）。仿真器走 loopback，不受该问题影响，
# 可独立完成样式/交互/依赖验证。
#
# 已装资源（本机实测）：
#   emulator 37.2.12.0 + system-images;android-30;android-wear-cn;arm64-v8a
#   AVD 名 godex_wear_cn（wearos_large_round，454x454，density 320，circular=yes）
#
# ⚠️ 重要事实：「中国版」镜像**仍内置 GMS**（指纹 google/sdk_gwear_sw_arm64）。
#    要复现「无 GMS」基线，必须用 `nogms` 子命令禁用 Google 包。
#
# 用法:
#   bash scripts/watch-emulator.sh create           # 创建 AVD（幂等）
#   bash scripts/watch-emulator.sh start            # 后台启动（headless）
#   bash scripts/watch-emulator.sh start -g         # 带窗口启动（人工预览）
#   bash scripts/watch-emulator.sh start -w         # 启动并等开机完成
#   bash scripts/watch-emulator.sh stop             # 停止
#   bash scripts/watch-emulator.sh status           # 设备与配置概览
#   bash scripts/watch-emulator.sh gates            # 跑门禁 0.1/0.2/0.3/0.7
#   bash scripts/watch-emulator.sh nogms            # 禁用 GMS（模拟无谷歌框架基线）
#   bash scripts/watch-emulator.sh withgms          # 恢复 GMS
#   bash scripts/watch-emulator.sh shot out.png     # 截图
#   bash scripts/watch-emulator.sh reset            # 冷启并清数据
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
JAVA_HOME="${JAVA_HOME:-$HOME/Library/Java/JavaVirtualMachines/jdk-21.0.12.1+1/Contents/Home}"
ADB="$ANDROID_HOME/platform-tools/adb"
EMU="$ANDROID_HOME/emulator/emulator"
SDKMANAGER="$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager"
AVDMANAGER="$ANDROID_HOME/cmdline-tools/latest/bin/avdmanager"
export JAVA_HOME ANDROID_HOME
export PATH="$ANDROID_HOME/cmdline-tools/latest/bin:$ANDROID_HOME/platform-tools:$PATH"

AVD_NAME="${AVD_NAME:-godex_wear_cn}"
SYS_IMG="${SYS_IMG:-system-images;android-30;android-wear-cn;arm64-v8a}"
DEVICE="${DEVICE:-wearos_large_round}"
SERIAL="${SERIAL:-emulator-5554}"

# 无 GMS 基线需禁用的包（用 pm disable-user，可逆）
GMS_PKGS=(com.google.android.gms com.android.vending com.google.android.gsf)

usage() { awk 'NR==1{next} /^[^#]/{exit} {sub(/^# ?/,""); print}' "$0"; exit "${1:-0}"; }

need() { [ -x "$1" ] || { echo "缺少 $1（先跑 create 或装 emulator 包）" >&2; exit 1; }; }

wait_boot() {
  echo -n "等待开机完成"
  for _ in $(seq 1 60); do
    if [ "$("$ADB" -s "$SERIAL" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then
      echo " ✔"; return 0
    fi
    echo -n "."; sleep 5
  done
  echo; echo "开机超时（60×5s）" >&2; exit 1
}

cmd=${1:-}
shift || true

case "$cmd" in
  create)
    need "$SDKMANAGER"
    echo "== 安装 emulator + 系统镜像（已存在则跳过）=="
    yes | "$SDKMANAGER" --install "emulator" "$SYS_IMG" 2>&1 | tail -3
    echo "== 创建 AVD: $AVD_NAME =="
    if "$AVDMANAGER" list avd 2>/dev/null | grep -q "Name: $AVD_NAME"; then
      echo "  已存在，跳过（要重建加 --force 并先删除）"
    else
      echo "no" | "$AVDMANAGER" create avd -n "$AVD_NAME" -k "$SYS_IMG" -d "$DEVICE" --force 2>&1 | tail -3
    fi
    # 圆屏 + 软渲染 + 足够内存（Compose 在 512M 下易 OOM）
    cfg="$HOME/.android/avd/$AVD_NAME.avd/config.ini"
    if [ -f "$cfg" ]; then
      python3 - "$cfg" <<'PY'
import re, sys
p = sys.argv[1]; s = open(p).read()
repl = {"hw.lcd.circular": "yes", "hw.gpu.enabled": "yes",
        "hw.gpu.mode": "swiftshader_indirect", "hw.ramSize": "1536",
        "hw.keyboard": "yes", "disk.dataPartition.size": "2048M"}
for k, v in repl.items():
    s = re.sub(rf"^{re.escape(k)}=.*$", f"{k}={v}", s, flags=re.M) if re.search(rf"^{re.escape(k)}=", s, re.M) else s + f"\n{k}={v}\n"
open(p, "w").write(s)
print("  已写圆屏/软渲染配置")
PY
    fi
    echo "== 完成。启动：bash scripts/watch-emulator.sh start -w =="
    ;;

  start)
    need "$EMU"
    if "$ADB" devices 2>/dev/null | grep -q "$SERIAL"; then
      echo "已在运行: $SERIAL"; exit 0
    fi
    # -g/--gui 打开仿真器窗口（用于人工预览）；默认无头，适合脚本与 CI。
    # 窗口模式下 GPU 交给 emulator 自选（auto 优先用宿主机硬件加速），
    # 无头模式仍固定软渲染以保证在无 GPU 的环境里也能起来。
    gui=0
    wait=0
    for arg in "$@"; do
      case "$arg" in
        -g|--gui) gui=1 ;;
        -w|--wait) wait=1 ;;
      esac
    done
    if [ "$gui" = "1" ]; then
      echo "== 启动 $AVD_NAME（带窗口，可人工预览）=="
      # 窗口模式不需要 -no-window，用空数组占位。
      win_args=()
      gpu="${WATCH_GPU:-auto}"
    else
      echo "== 启动 $AVD_NAME（headless，软渲染）=="
      win_args=(-no-window)
      gpu="swiftshader_indirect"
    fi
    echo "  提示：仿真器走 loopback，不受企业网络拦截影响"
    # 长驻进程：交给调用方后台化（nohup 或 background 工具），此处前台启动
    #
    # 注意 "${win_args[@]}" 的 ${arr[@]+...} 写法：macOS 自带 bash 3.2 在
    # set -u 下展开空数组会报 unbound variable，而窗口模式正是空数组。
    "$EMU" -avd "$AVD_NAME" ${win_args[@]+"${win_args[@]}"} -no-audio -no-boot-anim \
           -gpu "$gpu" -no-snapshot &
    emu_pid=$!
    echo "  emulator pid=$emu_pid"
    if [ "$wait" = "1" ]; then
      wait_boot
      "$0" status
    fi
    ;;

  stop)
    "$ADB" emu kill 2>/dev/null || true
    sleep 2
    pkill -f "emulator.*-avd $AVD_NAME" 2>/dev/null || true
    echo "已停止"
    "$ADB" devices
    ;;

  reset)
    need "$EMU"
    "$ADB" emu kill 2>/dev/null || true; sleep 2
    echo "== 冷启并清数据 =="
    "$EMU" -avd "$AVD_NAME" -no-window -no-audio -no-boot-anim \
           -gpu swiftshader_indirect -wipe-data &
    wait_boot
    ;;

  status)
    "$ADB" devices -l
    if ! "$ADB" devices 2>/dev/null | grep -q "$SERIAL"; then
      echo "仿真器未运行（bash scripts/watch-emulator.sh start）"; exit 0
    fi
    echo
    echo "--- 系统 ---"
    printf "  API      : %s\n" "$("$ADB" -s "$SERIAL" shell getprop ro.build.version.sdk | tr -d '\r')"
    printf "  release  : %s\n" "$("$ADB" -s "$SERIAL" shell getprop ro.build.version.release | tr -d '\r')"
    printf "  指纹     : %s\n" "$("$ADB" -s "$SERIAL" shell getprop ro.build.fingerprint | tr -d '\r')"
    printf "  镜像     : %s\n" "$(grep image.sysdir "$HOME/.android/avd/$AVD_NAME.avd/config.ini" | cut -d= -f2- | tr -d ' ')"
    echo "--- 屏幕 ---"
    printf "  尺寸     : %s\n" "$("$ADB" -s "$SERIAL" shell wm size | tr -d '\r' | sed 's/Physical size: //')"
    printf "  密度     : %s\n" "$("$ADB" -s "$SERIAL" shell wm density | tr -d '\r' | sed 's/Physical density: //')"
    printf "  圆形     : %s\n" "$("$ADB" -s "$SERIAL" shell getprop ro.emulator.circular | tr -d '\r')"
    echo "--- 宿主连通（10.0.2.2 = 宿主机）---"
    # 单次 ping 会瞬时误报 —— 重试 3 次，任一成功即视为可达。
    host_ok=0
    for _ in 1 2 3; do
      if "$ADB" -s "$SERIAL" shell ping -c 1 -W 2 10.0.2.2 2>/dev/null | grep -q "1 received"; then
        host_ok=1; break
      fi
      sleep 1
    done
    [ "$host_ok" = "1" ] && echo "  宿主可达 ✔（仿真器内可访问宿主上的 godex 服务）" \
                         || echo "  宿主不可达 ✘"
    ;;

  gates)
    need "$ADB"
    "$ADB" devices 2>/dev/null | grep -q "$SERIAL" || { echo "仿真器未运行" >&2; exit 1; }
    echo "=== 门禁验证（实施方案 §3.1）==="
    echo
    echo "--- 0.1 有无 GMS ---"
    if "$ADB" -s "$SERIAL" shell pm list packages -e 2>/dev/null | grep -q "com.google.android.gms"; then
      echo "  ⚠ 镜像内置 GMS（含 com.google.android.gms）"
      echo "    → 要复现「无 GMS」基线请跑：bash scripts/watch-emulator.sh nogms"
    else
      echo "  ✔ 无 GMS（符合基线）"
    fi
    echo
    echo "--- 0.2 API level / 圆形屏 ---"
    printf "  API=%s circular=%s density=%s\n" \
      "$("$ADB" -s "$SERIAL" shell getprop ro.build.version.sdk | tr -d '\r')" \
      "$("$ADB" -s "$SERIAL" shell getprop ro.emulator.circular | tr -d '\r')" \
      "$("$ADB" -s "$SERIAL" shell wm density 2>/dev/null | tr -d '\r' | awk '{print $NF}')"
    echo "  → minSdk 可定 30（Wear OS 3 基线）"
    echo
    echo "--- 0.3 侧载通路 ---"
    # 注意：不能用 `pm path com.android.settings` 判断 —— Wear OS 上没这个包，会误报。
    # 改为：pm 是否响应（能列出包）+ 是否已装有可验证的包。
    pkgs=$("$ADB" -s "$SERIAL" shell pm list packages 2>/dev/null | grep -c "^package:" || true)
    if [ "${pkgs:-0}" -gt 20 ]; then
      echo "  pm 响应正常，已装 ${pkgs} 个包 ✔"
      echo "  → adb install 可用（侧载通路成立）"
    else
      echo "  异常：pm 仅返回 ${pkgs} 个包 ✘"
    fi
    echo
    echo "--- 0.7 语音识别 provider ---"
    found=$("$ADB" -s "$SERIAL" shell pm query-activities --brief -a android.speech.RecognitionService 2>/dev/null | grep -c "package" || true)
    if [ "${found:-0}" -gt 0 ]; then
      echo "  ✔ 找到 $found 个识别服务 → SpeechRecognizer 可用作快路径"
    else
      echo "  ✘ 无 RecognitionService → SpeechRecognizer 不可用"
      echo "    → 语音主路径走 W9（录音 + voice-engine ASR）"
      echo "      快路径：运行时探测，可用则优先（不作依赖）"
    fi
    echo -n "  麦克风特性: "
    "$ADB" -s "$SERIAL" shell pm list features 2>/dev/null | grep -q "android.hardware.microphone" \
      && echo "有 ✔（MediaRecorder 录音可用）" || echo "无 ✘"
    ;;

  nogms)
    need "$ADB"
    echo "== 禁用 Google 包（模拟无 GMS 基线）=="
    for pkg in "${GMS_PKGS[@]}"; do
      out=$("$ADB" -s "$SERIAL" shell pm disable-user --user 0 "$pkg" 2>&1 | tr -d '\r')
      echo "  $pkg → $out"
    done
    echo
    echo "  验证："
    "$ADB" -s "$SERIAL" shell pm list packages -d 2>/dev/null | grep -E "$(IFS='|'; echo "${GMS_PKGS[*]}")" | sed 's/^/    已禁用 /'
    ;;

  withgms)
    need "$ADB"
    echo "== 恢复 Google 包 =="
    for pkg in "${GMS_PKGS[@]}"; do
      "$ADB" -s "$SERIAL" shell pm enable "$pkg" 2>&1 | tr -d '\r' | sed "s/^/  $pkg → /"
    done
    ;;

  shot)
    out=${1:-wear-emulator.png}
    need "$ADB"
    "$ADB" -s "$SERIAL" exec-out screencap -p > "$out"
    [ -s "$out" ] || { echo "截图失败" >&2; rm -f "$out"; exit 1; }
    echo "已保存: $out"
    ;;

  ""|-h|--help|help) usage 0 ;;
  *) echo "未知命令: $cmd" >&2; echo >&2; usage 1 ;;
esac
