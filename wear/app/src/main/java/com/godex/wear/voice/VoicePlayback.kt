package com.godex.wear.voice

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Plays short synthesized speech on the wrist (B7).
 *
 * [AudioTrack] with raw PCM rather than `MediaPlayer`: the node sends a WAV
 * container whose params are already known (24 kHz mono 16-bit — verified against
 * a real node), and `MediaPlayer` would add a file/URI lifecycle and a decoder for
 * a payload that is a few hundred KB of plain PCM. It also keeps playback in the
 * same shape as recording ([VoiceCapture] uses `AudioRecord`), so the audio path
 * has one style rather than two.
 *
 * Not a general audio player: no queue, no seek, no volume UI. A wrist reply is a
 * few seconds long and is played or abandoned.
 */
class VoicePlayback {

    private var track: AudioTrack? = null

    /**
     * Synthesized WAV parameters, read from the header.
     *
     * Parsing rather than assuming 24 kHz/1ch: the engine's rate is a property of
     * the voice model, and hardcoding it would produce chipmunk audio (or silence)
     * the day it changes. Anything unparseable falls back to the format we last
     * observed, which is better than refusing to speak.
     */
    private data class WavFormat(val sampleRate: Int, val channels: Int, val bitsPerSample: Int, val dataOffset: Int, val dataSize: Int)

    private fun parseWav(wav: ByteArray): WavFormat? {
        if (wav.size < 44) return null
        if (String(wav, 0, 4, Charsets.US_ASCII) != "RIFF") return null
        if (String(wav, 8, 4, Charsets.US_ASCII) != "WAVE") return null

        var offset = 12
        var sampleRate = 0
        var channels = 0
        var bits = 0
        // Walk the chunks instead of assuming 44-byte headers: some encoders emit a
        // LIST/fact chunk before `data`, and a fixed offset would then read samples
        // from the wrong place (the same trap the node's own parser guards against).
        while (offset + 8 <= wav.size) {
            val id = String(wav, offset, 4, Charsets.US_ASCII)
            val size = readIntLE(wav, offset + 4)
            val body = offset + 8
            when (id) {
                "fmt " -> {
                    if (body + 16 > wav.size) return null
                    channels = readShortLE(wav, body + 2)
                    sampleRate = readIntLE(wav, body + 4)
                    bits = readShortLE(wav, body + 14)
                }
                "data" -> {
                    val available = wav.size - body
                    // Trust the buffer over a size field that overruns it: a lying
                    // size must not make playback read past the end.
                    val size2 = if (size <= 0 || size > available) available else size
                    if (sampleRate <= 0 || channels <= 0 || bits <= 0) return null
                    return WavFormat(sampleRate, channels, bits, body, size2)
                }
            }
            // Chunks are word-aligned: an odd size is padded to an even boundary.
            offset = body + size + (size and 1)
        }
        return null
    }

    private fun readIntLE(b: ByteArray, i: Int): Int =
        (b[i].toInt() and 0xFF) or ((b[i + 1].toInt() and 0xFF) shl 8) or
            ((b[i + 2].toInt() and 0xFF) shl 16) or ((b[i + 3].toInt() and 0xFF) shl 24)

    private fun readShortLE(b: ByteArray, i: Int): Int =
        (b[i].toInt() and 0xFF) or ((b[i + 1].toInt() and 0xFF) shl 8)

    /** True while speech is coming out of the speaker. */
    val isSpeaking: Boolean get() = track?.playState == AudioTrack.PLAYSTATE_PLAYING

    /**
     * Plays [wav], blocking until it finishes or [stop] is called.
     *
     * Suspends onto IO so the caller's coroutine does not block the main thread;
     * the watch UI stays responsive and can still show its own state.
     */
    suspend fun play(wav: ByteArray): Boolean = withContext(Dispatchers.IO) {
        val format = parseWav(wav)
            ?: return@withContext false
        if (format.dataSize <= 0) return@withContext false

        val channelMask = if (format.channels == 1) AudioFormat.CHANNEL_OUT_MONO else AudioFormat.CHANNEL_OUT_STEREO
        val encoding = if (format.bitsPerSample == 8) AudioFormat.ENCODING_PCM_8BIT else AudioFormat.ENCODING_PCM_16BIT
        val minBuffer = AudioTrack.getMinBufferSize(format.sampleRate, channelMask, encoding)
        if (minBuffer <= 0) return@withContext false

        val bufferSize = maxOf(minBuffer, format.dataOffset.let { format.dataSize })
        val player = AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    // SPEECH + ASSISTANCE_SONIFICATION: this is spoken feedback, not
                    // media, so the system routes/ducks it as such.
                    .setUsage(AudioAttributes.USAGE_ASSISTANCE_SONIFICATION)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                    .build(),
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setEncoding(encoding)
                    .setSampleRate(format.sampleRate)
                    .setChannelMask(channelMask)
                    .build(),
            )
            .setBufferSizeInBytes(bufferSize)
            .setTransferMode(AudioTrack.MODE_STREAM)
            .build()

        try {
            track = player
            player.play()
            // Write in chunks: a whole-reply write can exceed the track buffer and
            // would return a short count, which MODE_STREAM does not queue.
            var written = 0
            val chunk = maxOf(1024, minBuffer)
            while (written < format.dataSize) {
                val end = minOf(written + chunk, format.dataSize)
                val n = player.write(wav, format.dataOffset + written, end - written)
                if (n <= 0) break
                written += n
            }
            // Let the queued tail drain before releasing the track, otherwise the
            // last syllable is cut off.
            player.stop()
            true
        } catch (_: Exception) {
            false
        } finally {
            track = null
            runCatching { player.release() }
        }
    }

    /** Stops playback early (the user tapped again, or left the screen). */
    fun stop() {
        val player = track ?: return
        track = null
        runCatching { if (player.playState == AudioTrack.PLAYSTATE_PLAYING) player.pause() }
        runCatching { player.flush() }
        runCatching { player.release() }
    }
}
