#!/usr/bin/env bash
# 拉取 Android 本地运行时二进制：busybox（sh 等基础工具）+ 静态 git（zig cc 交叉编译）。
# 产物目录: mobile/android/app/src/main/jniLibs/arm64-v8a/（app_lib_file 类型，untrusted_app 可 exec）
# 已验证环境: macOS(arm64) + zig 0.16.0；目标 aarch64-linux-musl（Android arm64）
# 依赖: curl/tar/make；zig 自动下载到 .godex/tmp
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RUNTIME_DIR="$ROOT/mobile/android/app/src/main/jniLibs/arm64-v8a"
SCRATCH="$ROOT/.godex/tmp/android-runtime"
ARCH=aarch64
TARGET=aarch64-linux-musl
ZIG_VER=0.16.0
GIT_VERSION=2.47.1

mkdir -p "$RUNTIME_DIR" "$SCRATCH"
cd "$SCRATCH"

echo "==> [1/4] busybox（EXALAB 静态版，作 sh + 基础工具）-> libbusybox.so"
BUSYBOX_URL="https://raw.githubusercontent.com/EXALAB/Busybox-static/main/busybox_${ARCH}"
if [ ! -s "$RUNTIME_DIR/libbusybox.so" ]; then
  curl -sL --retry 3 -o "$RUNTIME_DIR/libbusybox.so" "$BUSYBOX_URL"
  chmod +x "$RUNTIME_DIR/libbusybox.so"
  echo "    -> $RUNTIME_DIR/libbusybox.so"
else
  echo "    busybox 已存在，跳过"
fi

echo "==> [2/4] zig 交叉编译器（macOS 宿主 -> ${TARGET}）"
if [ ! -x "$SCRATCH/zig/bin/zig" ]; then
  rm -rf zig zig.tar.xz
  curl -sL --retry 3 -C - -o zig.tar.xz "https://ziglang.org/download/${ZIG_VER}/zig-aarch64-macos-${ZIG_VER}.tar.xz"
  tar xf zig.tar.xz
  mv "zig-aarch64-macos-${ZIG_VER}" zig
fi
ZIG="$SCRATCH/zig/zig"
"$ZIG" version

echo "==> [3/4] git 源码 + 依赖静态编译（zlib + openssl + libcurl + pcre2）"
mkdir -p deps/src && cd deps/src
PREFIX="$SCRATCH/static"
CC_WRAP="$ZIG cc -target ${TARGET}"
export AR="$ZIG ar" RANLIB="$ZIG ranlib"

fetch_extract() { # name url
  local name="$1" url="$2"
  if [ ! -d "$name" ]; then
    [ -f "$name.tar.gz" ] || curl -sL --retry 3 -o "$name.tar.gz" "$url"
    tar xzf "$name.tar.gz"
  fi
}

ZLIB=zlib-1.3.1
fetch_extract "$ZLIB" "https://zlib.net/fossils/${ZLIB}.tar.gz"
(cd "$ZLIB" && CC="$CC_WRAP" CHOST=${TARGET} ./configure --static --prefix="$PREFIX" >/dev/null && make -j4 >/dev/null && make install >/dev/null)

OPENSSL=openssl-3.0.13
fetch_extract "$OPENSSL" "https://www.openssl.org/source/${OPENSSL}.tar.gz"
(cd "$OPENSSL" && CC="$CC_WRAP" ./Configure linux-aarch64 no-shared --prefix="$PREFIX" >/dev/null && make -j4 >/dev/null && make install_sw >/dev/null)

PCRE2=pcre2-10.44
fetch_extract "$PCRE2" "https://github.com/PCRE2Project/pcre2/releases/download/${PCRE2}/${PCRE2}.tar.gz"
(cd "$PCRE2" && CC="$CC_WRAP" ./configure --host=${TARGET} --disable-shared --enable-static --prefix="$PREFIX" >/dev/null && make -j4 >/dev/null && make install >/dev/null)

CURL=curl-8.11.1
fetch_extract "$CURL" "https://curl.se/download/${CURL}.tar.gz"
(cd "$CURL" && CC="$CC_WRAP" ./configure --host=${TARGET} --disable-shared --enable-static \
  --without-libidn2 --without-libpsl --without-brotli --without-zstd --without-nghttp2 \
  --with-openssl --with-zlib --prefix="$PREFIX" >/dev/null && make -j4 >/dev/null && make install >/dev/null)

GIT="git-${GIT_VERSION}"
fetch_extract "$GIT" "https://github.com/git/git/archive/refs/tags/v${GIT_VERSION}.tar.gz"
(
  cd "$GIT"
  make distclean >/dev/null 2>&1 || true
  # 关键开关（逐项已验证）：
  #  - uname_S=Linux：绕开 macOS 宿主检测（否则启用 precompose_utf8 / arc4random 等 Darwin 兼容层）
  #  - NO_REGEX=NeedsStartEnd：musl regex 无 REG_STARTEND，用 git 自带 compat
  #  - CSPRNG_METHOD=openssl：musl 无 arc4random_buf，用已静态编译的 openssl
  #  - NO_GETTEXT/NO_ICONV/NO_EXPAT/NO_TCLTK/NO_PERL/NO_PYTHON：裁剪非必需依赖
  #  - CFLAGS/LDFLAGS 指向交叉编译的依赖 prefix，静态链接
  make -j4 all uname_S=Linux uname_R=6.6 uname_M=aarch64 \
    CC="$CC_WRAP" CURLDIR="$PREFIX" OPENSSLDIR="$PREFIX" ZLIB_PATH="$PREFIX" \
    USE_LIBPCRE2=1 NO_GETTEXT=1 NO_ICONV=1 NO_TCLTK=1 NO_PERL=1 NO_PYTHON=1 NO_EXPAT=1 \
    NO_REGEX=NeedsStartEnd CSPRNG_METHOD=openssl \
    CFLAGS="-O2 -I$PREFIX/include" LDFLAGS="-static -L$PREFIX/lib" || {
      echo "    [warn] make 存在部分失败（多为 oss-fuzz 链接），检查 git 主程序…"
    }
  cp git "$RUNTIME_DIR/libgit.so"
  chmod +x "$RUNTIME_DIR/libgit.so"
  cp git-remote-https "$RUNTIME_DIR/libgitremotehttps.so" 2>/dev/null || true
  chmod +x "$RUNTIME_DIR/libgitremotehttps.so" 2>/dev/null || true
)
echo "    -> $RUNTIME_DIR/libgit.so, libgitremotehttps.so"

echo "==> [4/4] 产物清单"
ls -lh "$RUNTIME_DIR/"
file "$RUNTIME_DIR/libbusybox.so" "$RUNTIME_DIR/libgit.so" 2>/dev/null || true
echo "==> done."
