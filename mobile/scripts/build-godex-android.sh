#!/usr/bin/env bash
# 交叉编译 godex 到 Android（GOOS=android/arm64），产出到 jniLibs。
# 用法: mobile/scripts/build-godex-android.sh
# 产物: mobile/android/app/src/main/jniLibs/arm64-v8a/libgodex.so
# 注意：必须用 GOOS=android 且放 jniLibs（app_lib_file 类型）——
#   小米 HyperOS SELinux 禁止 untrusted_app 执行 app_data_file 类型二进制，
#   nativeLibraryDir 是唯一 untrusted_app 可 exec 的 app 私有路径。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT_DIR="$ROOT/mobile/android/app/src/main/jniLibs/arm64-v8a"
mkdir -p "$OUT_DIR"

echo "==> 交叉编译 godex (GOOS=android GOARCH=arm64, CGO_ENABLED=0)"
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -o "$OUT_DIR/libgodex.so" "$ROOT/cmd/godex"
chmod +x "$OUT_DIR/libgodex.so"

ls -lh "$OUT_DIR/libgodex.so"
echo "==> done. 二进制已写入 $OUT_DIR/libgodex.so（jniLibs 直接进 APK）"
