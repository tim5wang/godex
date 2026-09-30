#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENGINE_DIR="${VOICE_ENGINE_DIR:-$ROOT/../voice-engine}"
if [[ "$ENGINE_DIR" != /* ]]; then
  ENGINE_DIR="$ROOT/$ENGINE_DIR"
fi
ENGINE_ADDR="${VOICE_ENGINE_ADDR:-127.0.0.1:17021}"
ASR_MODEL="${VOICE_ASR_MODEL:-asr/zipformer-small-zh-en}"
HEALTH_URL="http://${ENGINE_ADDR}/healthz"

if [[ ! -f "$ENGINE_DIR/go.mod" || ! -f "$ENGINE_DIR/cmd/voice-engine/main.go" ]]; then
  echo "[dev-voice] voice-engine checkout not found: $ENGINE_DIR" >&2
  echo "[dev-voice] set VOICE_ENGINE_DIR=/path/to/voice-engine" >&2
  exit 1
fi

check_model_files() {
  local missing=()
  local relative
  for relative in \
    models/vad/silero-v5/silero_vad.onnx \
    models/tts/kokoro-82m/model.onnx \
    models/tts/kokoro-82m/voices.bin \
    models/tts/kokoro-82m/tokens.txt \
    models/tts/kokoro-82m/espeak-ng-data/phontab; do
    [[ -f "$ENGINE_DIR/$relative" ]] || missing+=("$relative")
  done
  case "$ASR_MODEL" in
    asr/zipformer-small-zh-en)
      for relative in \
        models/asr/zipformer-small-zh-en/encoder-epoch-99-avg-1.int8.onnx \
        models/asr/zipformer-small-zh-en/decoder-epoch-99-avg-1.onnx \
        models/asr/zipformer-small-zh-en/joiner-epoch-99-avg-1.int8.onnx \
        models/asr/zipformer-small-zh-en/tokens.txt; do
        [[ -f "$ENGINE_DIR/$relative" ]] || missing+=("$relative")
      done
      ;;
    asr/sensevoice-small-int8)
      for relative in models/asr/sensevoice-small-int8/model.int8.onnx models/asr/sensevoice-small-int8/tokens.txt; do
        [[ -f "$ENGINE_DIR/$relative" ]] || missing+=("$relative")
      done
      ;;
    asr/vosk-zh-small)
      [[ -d "$ENGINE_DIR/models/asr/vosk-zh-small/am" ]] || missing+=("models/asr/vosk-zh-small/am")
      ;;
    *)
      echo "[dev-voice] unsupported VOICE_ASR_MODEL: $ASR_MODEL" >&2
      echo "[dev-voice] supported values: asr/zipformer-small-zh-en, asr/sensevoice-small-int8, asr/vosk-zh-small" >&2
      return 2
      ;;
  esac
  if ((${#missing[@]} > 0)); then
    echo "[dev-voice] required model files are missing:" >&2
    printf '  %s\n' "${missing[@]}" >&2
    local fetch_asr="zipformer"
    [[ "$ASR_MODEL" == "asr/sensevoice-small-int8" ]] && fetch_asr="sensevoice"
    [[ "$ASR_MODEL" == "asr/vosk-zh-small" ]] && fetch_asr="vosk"
    echo "[dev-voice] fetch them explicitly with:" >&2
    echo "  cd \"$ENGINE_DIR\" && ./scripts/fetch-models.sh --asr $fetch_asr" >&2
    return 1
  fi
}

check_model_files

ENGINE_PID=""
ENGINE_TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/godex-voice.XXXXXX")"
ENGINE_LOG="$ENGINE_TMP_DIR/voice-engine.log"
ENGINE_BINARY="$ENGINE_TMP_DIR/voice-engine"
ENGINE_CHECKER="$ENGINE_TMP_DIR/voice-engine-check"
cleanup() {
  if [[ -n "$ENGINE_PID" ]] && kill -0 "$ENGINE_PID" 2>/dev/null; then
    kill "$ENGINE_PID" 2>/dev/null || true
    wait "$ENGINE_PID" 2>/dev/null || true
  fi
  rm -f "$ENGINE_LOG" "$ENGINE_BINARY" "$ENGINE_CHECKER"
  rmdir "$ENGINE_TMP_DIR" 2>/dev/null || true
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "[dev-voice] building lightweight WebSocket readiness checker"
(
  cd "$ENGINE_DIR"
  go build -o "$ENGINE_CHECKER" ./cmd/voice-engine-check
)

health="$(curl -fsS --max-time 1 "$HEALTH_URL" 2>/dev/null || true)"
if [[ "$health" == *'"ready":true'* ]]; then
  if ! "$ENGINE_CHECKER" -addr "$ENGINE_ADDR"; then
    echo "[dev-voice] health endpoint is ready but the WebSocket hello/ready handshake failed at $ENGINE_ADDR" >&2
    exit 1
  fi
  echo "[dev-voice] reusing ready voice-engine at $ENGINE_ADDR"
else
  echo "[dev-voice] building voice-engine"
  (
    cd "$ENGINE_DIR"
    CGO_ENABLED=1 go build -o "$ENGINE_BINARY" ./cmd/voice-engine
  )
  echo "[dev-voice] starting voice-engine ($ASR_MODEL) at $ENGINE_ADDR"
  (
    cd "$ENGINE_DIR"
    exec "$ENGINE_BINARY" -addr "$ENGINE_ADDR" -asr "$ASR_MODEL"
  ) >"$ENGINE_LOG" 2>&1 &
  ENGINE_PID=$!

  ready=0
  for _ in $(seq 1 90); do
    if ! kill -0 "$ENGINE_PID" 2>/dev/null; then
      cat "$ENGINE_LOG" >&2
      echo "[dev-voice] voice-engine exited before becoming ready" >&2
      exit 1
    fi
    health="$(curl -fsS --max-time 1 "$HEALTH_URL" 2>/dev/null || true)"
    if [[ "$health" == *'"ready":true'* ]]; then
      if ! "$ENGINE_CHECKER" -addr "$ENGINE_ADDR"; then
        cat "$ENGINE_LOG" >&2
        echo "[dev-voice] health endpoint is ready but the WebSocket hello/ready handshake failed at $ENGINE_ADDR" >&2
        exit 1
      fi
      ready=1
      break
    fi
    sleep 1
  done
  if ((ready == 0)); then
    cat "$ENGINE_LOG" >&2
    echo "[dev-voice] timed out waiting for voice-engine readiness at $HEALTH_URL" >&2
    exit 1
  fi
fi

echo "[dev-voice] voice-engine ready; rebuilding and restarting Godex"
make dev PPROF_ADDR="${PPROF_ADDR:-}"

if [[ -n "$ENGINE_PID" ]]; then
  echo "[dev-voice] Godex restarted. Press Ctrl-C to stop the voice-engine started by this command."
  wait "$ENGINE_PID"
fi
