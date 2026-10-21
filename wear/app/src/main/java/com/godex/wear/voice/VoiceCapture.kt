package com.godex.wear.voice

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import androidx.core.content.ContextCompat
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import java.io.ByteArrayOutputStream
import java.io.File
import kotlin.coroutines.coroutineContext
import kotlin.math.max
import kotlin.math.min

/**
 * Dictation on a watch that may have no on-device speech service.
 *
 * Two paths, in this order:
 *
 *  1. **Fast path** — the platform [SpeechRecognizer], used only when the device
 *     actually advertises a recognition service. It keeps audio on-device, which
 *     saves a round-trip and battery.
 *  2. **Main path** — raw capture via [AudioRecord] uploaded to the node, which
 *     transcribes it with the existing voice-engine ASR.
 *
 * Why the fallback is the *main* path: the Wear OS emulator used for verification
 * exposes no `RecognitionService` at all, so `SpeechRecognizer` is unusable
 * there and equally absent on any device without a Google or vendor speech
 * stack. [AudioRecord] only needs the microphone hardware, which is always
 * present.
 *
 * [AudioRecord] is used rather than `MediaRecorder` because the engine expects
 * 16 kHz mono PCM, and `MediaRecorder` can only emit container formats
 * (MP4/3GP) that would then need decoding on the node.
 */
class VoiceCapture(private val context: Context) {

    private var recorder: AudioRecord? = null
    private var recording = false

    /** True when a platform recogniser exists and can be preferred. */
    fun hasSpeechRecognizer(): Boolean =
        runCatching { SpeechRecognizer.isRecognitionAvailable(context) }.getOrDefault(false)

    fun hasMicrophonePermission(): Boolean =
        ContextCompat.checkSelfPermission(context, Manifest.permission.RECORD_AUDIO) ==
            PackageManager.PERMISSION_GRANTED

    /**
     * Intent for the fast path. Returns null when no recogniser is available so
     * the caller can go straight to recording.
     */
    fun recognitionIntent(): android.content.Intent? {
        if (!hasSpeechRecognizer()) return null
        return android.content.Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            putExtra(RecognizerIntent.EXTRA_LANGUAGE, "zh-CN")
            putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, false)
        }
    }

    /**
     * Starts capture. Returns false when the microphone is unavailable or the
     * permission is missing, so the UI can explain rather than silently no-op.
     */
    fun start(): Boolean {
        if (recording) return true
        if (!hasMicrophonePermission()) return false

        val minBuffer = AudioRecord.getMinBufferSize(SAMPLE_RATE, CHANNEL, ENCODING)
        if (minBuffer <= 0) return false
        // Several min-buffer units: a single one overflows between read() calls
        // and produces choppy audio on a busy watch CPU.
        val bufferSize = minBuffer * 4

        val record = runCatching {
            AudioRecord(MediaRecorder.AudioSource.MIC, SAMPLE_RATE, CHANNEL, ENCODING, bufferSize)
        }.getOrNull() ?: return false
        if (record.state != AudioRecord.STATE_INITIALIZED) {
            record.release()
            return false
        }

        return runCatching {
            record.startRecording()
            recorder = record
            recording = true
            true
        }.getOrDefault(false).also { ok ->
            if (!ok) runCatching { record.release() }
        }
    }

    /**
     * Stops capture and returns a 16 kHz mono WAV file, or null if nothing was
     * captured (e.g. the user tapped without speaking).
     */
    suspend fun stop(): File? = withContext(Dispatchers.IO) {
        val record = recorder ?: return@withContext null
        recorder = null
        recording = false

        val pcm = ByteArrayOutputStream()
        val chunk = ByteArray(4096)
        try {
            // Drain whatever is buffered before stopping, otherwise the tail of
            // the utterance is lost.
            while (true) {
                val read = record.read(chunk, 0, chunk.size)
                if (read <= 0) break
                pcm.write(chunk, 0, read)
            }
        } catch (_: Exception) {
            // Fall through: a partial recording is still worth transcribing.
        } finally {
            runCatching { record.stop() }
            runCatching { record.release() }
        }

        val bytes = pcm.toByteArray()
        // ~0.25s at 16 kHz mono s16; below that it is almost certainly a
        // mis-tap and uploading it would just waste the radio.
        if (bytes.size < SAMPLE_RATE * 2 / 4) return@withContext null

        val out = File(context.cacheDir, "dictation-${System.currentTimeMillis()}.wav")
        out.outputStream().use { it.write(wavHeader(bytes.size) + bytes) }
        out
    }

    /** Continuous read loop used only for level metering in the UI. */
    suspend fun levelMeter(): Int = withContext(Dispatchers.Default) {
        val record = recorder ?: return@withContext 0
        val buf = ByteArray(2048)
        var peak = 0
        while (coroutineContext.isActive && recording) {
            val read = runCatching { record.read(buf, 0, buf.size) }.getOrDefault(0)
            if (read <= 0) break
            for (i in 0 until read - 1 step 2) {
                // Little-endian s16 sample; only the magnitude matters here.
                val sample = (buf[i].toInt() and 0xFF) or (buf[i + 1].toInt() shl 8)
                peak = max(peak, min(100, kotlin.math.abs(sample) * 100 / 32768))
            }
            break
        }
        peak
    }

    fun cancel() {
        recording = false
        val record = recorder ?: return
        recorder = null
        runCatching { record.stop() }
        runCatching { record.release() }
    }

    /** Standard 44-byte RIFF/WAVE header for s16 mono PCM. */
    private fun wavHeader(dataLength: Int): ByteArray {
        val byteRate = SAMPLE_RATE * CHANNELS * BITS_PER_SAMPLE / 8
        val blockAlign = CHANNELS * BITS_PER_SAMPLE / 8
        val header = ByteArray(44)

        fun putAscii(offset: Int, value: String) {
            value.forEachIndexed { i, c -> header[offset + i] = c.code.toByte() }
        }
        fun putIntLE(offset: Int, value: Int) {
            header[offset] = (value and 0xFF).toByte()
            header[offset + 1] = ((value shr 8) and 0xFF).toByte()
            header[offset + 2] = ((value shr 16) and 0xFF).toByte()
            header[offset + 3] = ((value shr 24) and 0xFF).toByte()
        }
        fun putShortLE(offset: Int, value: Int) {
            header[offset] = (value and 0xFF).toByte()
            header[offset + 1] = ((value shr 8) and 0xFF).toByte()
        }

        putAscii(0, "RIFF")
        putIntLE(4, 36 + dataLength)
        putAscii(8, "WAVE")
        putAscii(12, "fmt ")
        putIntLE(16, 16)          // PCM fmt chunk size
        putShortLE(20, 1)         // format = PCM
        putShortLE(22, CHANNELS)
        putIntLE(24, SAMPLE_RATE)
        putIntLE(28, byteRate)
        putShortLE(32, blockAlign)
        putShortLE(34, BITS_PER_SAMPLE)
        putAscii(36, "data")
        putIntLE(40, dataLength)
        return header
    }

    private companion object {
        const val SAMPLE_RATE = 16000
        const val CHANNEL = AudioFormat.CHANNEL_IN_MONO
        const val ENCODING = AudioFormat.ENCODING_PCM_16BIT
        const val CHANNELS = 1
        const val BITS_PER_SAMPLE = 16
    }
}
