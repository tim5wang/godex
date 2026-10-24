#!/usr/bin/env bash
# 手表无线调试封装：配对 / 连接 / 装包 / 截图 / 日志。
#
# 依赖：Android SDK platform-tools（adb ≥ 30，原生支持无线调试）。
# 本脚本不安装任何东西，只把 adb 的无线调试流程串成一条命令。
#
# 手表侧准备（一次性）：
#   设置 → 关于手表 → 连点「版本号」7 次 → 开发者选项 → 打开「无线调试」
#   （小米 HyperOS 路径可能为：设置 → 系统 → 关于 → 版本号）
#   进入「无线调试」页 → 「使用配对码配对设备」→ 屏幕上会显示 IP:端口 + 6 位配对码
#
# 用法:
#   bash scripts/watch-adb.sh discover                    # 自动发现同一 WiFi 下的手表
#   bash scripts/watch-adb.sh pair 192.168.1.23:37123 123456
#   bash scripts/watch-adb.sh connect                     # 自动取 connect 端口并连接
#   bash scripts/watch-adb.sh connect 192.168.1.23:5555   # 手动指定
#   bash scripts/watch-adb.sh status                      # 列出已连设备
#   bash scripts/watch-adb.sh install path/to/app.apk     # 安装（缺省找 debug APK）
#   bash scripts/watch-adb.sh shot out.png                # 截图拉回本地
#   bash scripts/watch-adb.sh logcat                      # 过滤 godex 相关日志
#   bash scripts/watch-adb.sh shell                       # 进设备 shell
#   bash scripts/watch-adb.sh net <IP:端口>               # 诊断可达性（VPN 劫持/路由/端口）
#   bash scripts/watch-adb.sh up <配对IP:端口> <码> [连接IP:端口]  # 一键：重启 server → 配对 → 连接 → 体检
#   bash scripts/watch-adb.sh try <IP:端口>                 # 直接 connect（已配对过/端口已是连接端口时用）
#   bash scripts/watch-adb.sh disconnect                  # 断开所有 TCP 设备
#   bash scripts/watch-adb.sh path                        # 打印 PATH 配置建议
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
ADB="${ADB:-$ANDROID_HOME/platform-tools/adb}"

if [ ! -x "$ADB" ]; then
  echo "找不到 adb: $ADB" >&2
  echo "请设置 ANDROID_HOME 指向 Android SDK，或直接指定 ADB=/path/to/adb" >&2
  exit 1
fi

# 只打印文件头部的注释块（跳过 shebang，遇到首个非注释行停止），
# 避免把 set -euo pipefail 之类的实现细节也打出来。
usage() { awk 'NR==1{next} /^[^#]/{exit} {sub(/^# ?/,""); print}' "$0"; exit "${1:-0}"; }

# 从 `adb mdns services` 里取某项服务的地址（输出最后一列为 <ip>:<port>）。
# 真实输出形如：
#   List of discovered mdns services
#   adb-R58T...-abcdef  _adb-tls-connect._tcp  192.168.1.23:5555
# 服务类型在第 2 列，因此匹配整行而非固定某列。
mdns_addr() {
  local pattern=$1
  "$ADB" mdns services 2>/dev/null | awk -v pat="$pattern" '$0 ~ pat {print $NF; exit}'
}

require_device() {
  local n
  n=$("$ADB" devices | awk 'NR>1 && $2=="device"' | wc -l | tr -d ' ')
  if [ "$n" = "0" ]; then
    echo "没有已连接的设备。先执行：bash scripts/watch-adb.sh pair <IP:端口> <配对码>" >&2
    echo "再执行：bash scripts/watch-adb.sh connect" >&2
    exit 1
  fi
}

cmd=${1:-}
shift || true

case "$cmd" in
  discover)
    echo "== mDNS 发现（手表需已打开「无线调试」且与电脑同一 WiFi）=="
    "$ADB" mdns check >/dev/null 2>&1 || true
    services=$("$ADB" mdns services 2>/dev/null || true)
    if [ -z "$services" ]; then
      echo "未发现服务。排查：① 手表无线调试是否打开 ② 是否同一网段 ③ 防火墙"
      exit 1
    fi
    echo "$services"
    echo
    pairing=$(mdns_addr '_adb-tls-pairing' || true)
    connect=$(mdns_addr '_adb-tls-connect' || true)
    [ -n "$pairing" ] && echo "配对用地址: $pairing"
    [ -n "$connect" ] && echo "连接用地址: $connect"
    ;;

  pair)
    addr=${1:-}
    code=${2:-}
    if [ -z "$addr" ] || [ -z "$code" ]; then
      addr=${addr:-$(mdns_addr '_adb-tls-pairing' || true)}
      if [ -z "$addr" ]; then
        echo "用法: bash scripts/watch-adb.sh pair <IP:端口> <配对码>" >&2
        echo "（自动发现失败，请照手表「使用配对码配对设备」页手输）" >&2
        exit 1
      fi
      echo "自动发现配对地址: $addr"
    fi
    [ -n "$code" ] || { echo "缺少配对码（手表屏幕上的 6 位数字）" >&2; exit 1; }
    echo "== 配对 $addr =="
    "$ADB" pair "$addr" "$code"
    echo
    echo "配对成功。接着执行：bash scripts/watch-adb.sh connect"
    ;;

  connect)
    addr=${1:-}
    if [ -z "$addr" ]; then
      addr=$(mdns_addr '_adb-tls-connect' || true)
      if [ -n "$addr" ]; then
        echo "自动发现连接地址: $addr"
      else
        # 回退：配对地址已知时，连接端口通常同网段
        echo "自动发现失败，请手动指定：bash scripts/watch-adb.sh connect <IP:端口>" >&2
        exit 1
      fi
    fi
    echo "== 连接 $addr =="
    "$ADB" connect "$addr"
    echo
    "$ADB" devices -l
    ;;

  up)
    pair_addr=${1:-}; code=${2:-}; conn_addr=${3:-}
    if [ -z "$pair_addr" ] || [ -z "$code" ]; then
      echo "用法: bash scripts/watch-adb.sh up <配对IP:端口> <配对码> [连接IP:端口]" >&2
      exit 1
    fi
    echo "== 1/4 重启 adb server（清掉 VPN 切换后残留状态）=="
    "$ADB" kill-server >/dev/null 2>&1 || true
    sleep 1
    "$ADB" start-server >/dev/null 2>&1 || true

    echo "== 2/4 配对 $pair_addr =="
    if ! "$ADB" pair "$pair_addr" "$code"; then
      echo >&2
      echo "配对失败。按概率排查：" >&2
      echo "  1) 端口用错：必须用「使用配对码配对设备」弹窗里的端口，" >&2
      echo "     不是「无线调试」主页面上那个端口（那个是连接端口）。" >&2
      echo "  2) 弹窗已关/超时（约 5 分钟）→ 重新打开取新码。" >&2
      echo "  3) 路由被 VPN 劫持 → 先跑: bash scripts/watch-adb.sh net $pair_addr" >&2
      exit 1
    fi

    echo "== 3/4 连接 =="
    [ -n "$conn_addr" ] || conn_addr=$(mdns_addr '_adb-tls-connect' || true)
    if [ -n "$conn_addr" ]; then
      "$ADB" connect "$conn_addr" || true
    else
      echo "  mDNS 未发现连接端口（本机 mDNS 为空，可能未授予「本地网络」权限）。"
      echo "  请用「无线调试」主页面的 IP:端口 手动连接："
      echo "    bash scripts/watch-adb.sh connect <IP:端口>"
    fi

    echo "== 4/4 体检 =="
    "$0" status
    ;;

  try)
    addr=${1:-}
    if [ -z "$addr" ]; then
      echo "用法: bash scripts/watch-adb.sh try <IP:端口>" >&2
      echo "（适用于：已配对过，或该端口本身就是连接端口）" >&2
      exit 1
    fi
    "$ADB" connect "$addr" || true
    "$0" status
    ;;

  net|doctor)
    target=${1:-}
    echo "== 网络诊断：无线调试可达性 =="
    echo
    lan_ip=$(ipconfig getifaddr en0 2>/dev/null || true)
    echo "本机 en0: ${lan_ip:-未取到}"
    echo
    echo "--- 默认路由 ---"
    netstat -rn -f inet 2>/dev/null | awk '$1=="default"{printf "  %-8s %-16s %-8s %s\n",$1,$2,$4,$NF}'
    echo
    if [ -n "$lan_ip" ]; then
      prefix=$(echo "$lan_ip" | cut -d. -f1-3)
      echo "--- 本网段 $prefix.0/24 路由（被隧道抢占则局域网不可达）---"
      netstat -rn -f inet 2>/dev/null | awk -v p="$prefix" '$1==p || index($1,p".")==1 {printf "  %-20s %-8s %s\n",$1,$4,$NF}'
      echo
    fi
    if [ -n "$target" ]; then
      host=${target%%:*}
      port=${target##*:}
      [ "$host" = "$port" ] && port=5555
      echo "--- 到 $target 的路由 ---"
      iface=$(route -n get "$host" 2>/dev/null | awk '/interface:/{print $2}')
      gw=$(route -n get "$host" 2>/dev/null | awk '/gateway:/{print $2}')
      echo "  接口: ${iface:-?}   网关: ${gw:-直连}"
      case "$iface" in
        utun*|ppp*|ipsec*)
          echo "  ✘ 流量被 VPN 隧道（${iface}）劫持 → adb 无法到达设备。"
          echo "    解法：① 改用手机热点（Mac+手表同热点，绕开被抢占网段）"
          echo "          ② sudo route add -host $host -interface en0（需密码，VPN 重连后失效）"
          echo "          ③ 临时断开公司 VPN"
          ;;
        en*|bridge*)
          echo "  ✔ 走本机网卡 ${iface}（路由正常）"
          ;;
        "")
          echo "  ? 未取到接口（主机名解析或网络不可用）"
          ;;
      esac
      echo
      echo "--- ARP（二层是否可见）---"
      arp -n "$host" 2>/dev/null | sed 's/^/  /' || echo "  无条目"
      echo
      echo "--- TCP 探测 ---"
      if command -v nc >/dev/null 2>&1; then
        if nc -z -w 3 "$host" "$port" 2>/dev/null; then
          echo "  ✔ $host:$port 可达"
        else
          echo "  ✘ $host:$port 不可达（配对/连接会失败）"
        fi
      else
        echo "  （无 nc，跳过）"
      fi
      echo
    fi
    echo "--- 提示 ---"
    echo "  · 配对成功后 adb connect 走同一条路；本文档诊断同样适用"
    echo "  · macOS 15+ 需给终端/宿主进程「本地网络」权限：系统设置 → 隐私与安全性 → 本地网络"
    ;;

  status|devices)
    "$ADB" devices -l
    echo
    n=$("$ADB" devices | awk 'NR>1 && $2=="device"' | wc -l | tr -d ' ')
    echo "已连接设备数: $n"
    if [ "$n" != "0" ]; then
      echo "--- 设备信息 ---"
      echo "型号:   $("$ADB" shell getprop ro.product.model 2>/dev/null | tr -d '\r')"
      echo "系统:   $("$ADB" shell getprop ro.build.version.release 2>/dev/null | tr -d '\r')"
      echo "API:    $("$ADB" shell getprop ro.build.version.sdk 2>/dev/null | tr -d '\r')"
      echo "--- 门禁 0.1：GMS 检查（PRD §3）---"
      if "$ADB" shell pm list packages 2>/dev/null | grep -q "com.google.android.gms"; then
        echo "✔ 检测到 GMS → FCM 推送可用，推送方案可放宽"
      else
        echo "✘ 未检测到 GMS → 推送必须走手机端桥接（实施方案 §6）"
      fi
    fi
    ;;

  install)
    apk=${1:-}
    if [ -z "$apk" ]; then
      for cand in \
        "$ROOT/wear/app/build/outputs/apk/debug/app-debug.apk" \
        "$ROOT/mobile/android/app/build/outputs/apk/debug/app-debug.apk"; do
        if [ -f "$cand" ]; then apk="$cand"; break; fi
      done
    fi
    [ -n "$apk" ] && [ -f "$apk" ] || {
      echo "未找到 APK。用法: bash scripts/watch-adb.sh install <path/to/app.apk>" >&2
      exit 1
    }
    require_device
    echo "== 安装 $apk =="
    ls -lh "$apk" | awk '{print "    size:", $5}'
    "$ADB" install -r "$apk"
    ;;

  shot)
    out=${1:-watch-shot.png}
    require_device
    "$ADB" exec-out screencap -p > "$out"
    [ -s "$out" ] || { echo "截图失败（文件为空）" >&2; rm -f "$out"; exit 1; }
    echo "已保存: $out"
    ls -lh "$out" | awk '{print "    size:", $5}'
    ;;

  logcat)
    require_device
    echo "== logcat（过滤 godex，Ctrl-C 退出）=="
    "$ADB" logcat -v time | grep -iE "godex|wear|AndroidRuntime|FATAL" || true
    ;;

  shell)
    require_device
    shift || true
    exec "$ADB" shell "$@"
    ;;

  disconnect)
    "$ADB" disconnect
    ;;

  path)
    echo "把 platform-tools 加入 PATH（追加到 ~/.zshrc）:"
    echo
    echo "    export PATH=\"\$HOME/Library/Android/sdk/platform-tools:\$PATH\""
    echo
    echo "当前 adb: $ADB"
    "$ADB" version | head -2
    ;;

  ""|-h|--help|help)
    usage 0
    ;;

  *)
    echo "未知命令: $cmd" >&2
    echo >&2
    usage 1
    ;;
esac
