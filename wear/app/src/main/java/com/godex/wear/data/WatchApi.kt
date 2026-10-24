package com.godex.wear.data

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.Json
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.MultipartBody
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.asRequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.File
import java.io.IOException
import java.util.concurrent.TimeUnit

/** Error carrying the HTTP status so callers can branch on 401 vs 409 etc. */
class WatchApiException(
    val statusCode: Int,
    override val message: String,
) : IOException(message) {
    /** A 401/403 means the stored credential is gone or was revoked. */
    val isAuthFailure: Boolean get() = statusCode == 401 || statusCode == 403
}

/**
 * Thin client over the godex watch surface (`/watch/...`).
 *
 * Deliberately synchronous + explicit: the watch UI calls these from a
 * coroutine and renders the result. No caching layer or repository abstraction,
 * because the whole surface is a handful of endpoints and an extra indirection
 * would only make the data flow harder to follow.
 *
 * Timeouts are tuned for a watch on a flaky BLE-tethered link: short connect,
 * moderate read, and no retries (a retry storm costs battery and the UI already
 * offers an explicit refresh).
 */
class WatchApi(
    private val client: OkHttpClient = defaultClient(),
    private val json: Json = defaultJson(),
) {
    @Volatile
    var baseUrl: String = ""
    @Volatile
    var token: String = ""

    private fun url(path: String): String = baseUrl.trimEnd('/') + path

    /**
     * Runs [block] and converts transport/HTTP problems into [WatchApiException].
     * Every public call funnels through here so error handling stays uniform.
     */
    private suspend inline fun <reified T> call(
        crossinline block: () -> Request.Builder,
    ): T = withContext(Dispatchers.IO) {
        val request = block().apply {
            if (token.isNotEmpty()) header("Authorization", "Bearer $token")
        }.build()

        client.newCall(request).execute().use { response ->
            val body = response.body?.string().orEmpty()
            if (!response.isSuccessful) {
                val detail = runCatching { json.decodeFromString<ErrorBody>(body).error }
                    .getOrNull()
                    ?.takeIf { it.isNotBlank() }
                    ?: "request failed"
                throw WatchApiException(response.code, detail)
            }
            if (body.isBlank()) {
                // Endpoints that return no content (e.g. revoke) still need a
                // value when T is non-Unit; surface a clear error instead of a
                // confusing deserialization failure.
                throw WatchApiException(response.code, "empty response body")
            }
            runCatching { json.decodeFromString<T>(body) }.getOrElse { err ->
                throw WatchApiException(response.code, "unexpected response: ${err.message}")
            }
        }
    }

    /**
     * Executes a request whose response body is not needed.
     *
     * Separate from [call] because approve/deny return a resolution object that
     * the watch does not render; forcing it through a generic serializer would
     * make the call fail on any field mismatch for no benefit.
     */
    private suspend inline fun execute(
        crossinline block: () -> Request.Builder,
    ): Unit = withContext(Dispatchers.IO) {
        val request = block().apply {
            if (token.isNotEmpty()) header("Authorization", "Bearer $token")
        }.build()

        client.newCall(request).execute().use { response ->
            if (response.isSuccessful) return@withContext
            val body = response.body?.string().orEmpty()
            val detail = runCatching { json.decodeFromString<ErrorBody>(body).error }
                .getOrNull()
                ?.takeIf { it.isNotBlank() }
                ?: "request failed"
            throw WatchApiException(response.code, detail)
        }
    }

    /**
     * Executes a request whose response is a binary payload (audio).
     *
     * A third path alongside [call]/[execute]: [call] would try to decode the body
     * as JSON, and [execute] discards it. Kept separate so neither grows a
     * "sometimes binary" branch.
     */
    private suspend inline fun rawBytes(
        crossinline block: () -> Request.Builder,
    ): ByteArray = withContext(Dispatchers.IO) {
        val request = block().apply {
            if (token.isNotEmpty()) header("Authorization", "Bearer $token")
        }.build()

        client.newCall(request).execute().use { response ->
            if (!response.isSuccessful) {
                // Errors from this surface are JSON, so read them as text for the
                // message the UI shows.
                val body = response.body?.string().orEmpty()
                val detail = runCatching { json.decodeFromString<ErrorBody>(body).error }
                    .getOrNull()
                    ?.takeIf { it.isNotBlank() }
                    ?: "request failed"
                throw WatchApiException(response.code, detail)
            }
            response.body?.bytes()
                ?: throw WatchApiException(response.code, "empty audio response")
        }
    }

    // ---- pairing (no credential required) ----

    suspend fun startPairing(name: String, kind: String = "watch"): PairingStart {
        val payload = json.encodeToString(
            kotlinx.serialization.json.JsonObject.serializer(),
            kotlinx.serialization.json.buildJsonObject {
                put("name", kotlinx.serialization.json.JsonPrimitive(name))
                put("kind", kotlinx.serialization.json.JsonPrimitive(kind))
            },
        )
        return call {
            Request.Builder()
                .url(url("/watch/pair/start"))
                .post(payload.toRequestBody(JSON_MEDIA))
        }
    }

    /** Polls for the issued credential. Pending is a normal result, not an error. */
    suspend fun exchangeToken(deviceCode: String): PairingToken {
        val payload = json.encodeToString(
            kotlinx.serialization.json.JsonObject.serializer(),
            kotlinx.serialization.json.buildJsonObject {
                put("device_code", kotlinx.serialization.json.JsonPrimitive(deviceCode))
            },
        )
        return call {
            Request.Builder()
                .url(url("/watch/pair/token"))
                .post(payload.toRequestBody(JSON_MEDIA))
        }
    }

    // ---- read surface ----

    suspend fun overview(): WatchOverview = call {
        Request.Builder().url(url("/watch/overview")).get()
    }

    suspend fun sessions(limit: Int = 20): List<WatchSession> = call {
        Request.Builder().url(url("/watch/sessions?limit=$limit")).get()
    }

    suspend fun sessionDetail(sessionId: String): WatchSessionDetail = call {
        Request.Builder().url(url("/watch/sessions/$sessionId")).get()
    }

    suspend fun nodes(): List<WatchNode> = call {
        Request.Builder().url(url("/watch/nodes")).get()
    }

    suspend fun approvals(): List<WatchApproval> = call {
        Request.Builder().url(url("/watch/approvals")).get()
    }

    /**
     * Every session this center can see, tagged with its owning node (mockup 14).
     *
     * The server returns the unfiltered list because "filter by device" is a
     * client-side view; narrowing locally avoids one round-trip per filter tap.
     */
    suspend fun crossDeviceSessions(): List<WatchCrossDeviceSession> = call {
        Request.Builder().url(url("/watch/cross-device")).get()
    }

    /**
     * Presets the wrist may start a conversation with.
     *
     * Curated server-side: the node can hold dozens of templates, most of which
     * assume a full-size screen.
     */
    suspend fun templates(): List<WatchTemplate> = call {
        Request.Builder().url(url("/watch/templates")).get()
    }

    /**
     * Starts a new conversation.
     *
     * An empty [templateId] means "the node's watch preset", which is why the
     * signature does not require one: creating from a wrist must not be a
     * configuration exercise.
     */
    suspend fun createSession(templateId: String = "", title: String = ""): WatchSession = call {
        val payload = json.encodeToString(
            CreateSessionRequest.serializer(),
            CreateSessionRequest(templateId = templateId, title = title),
        )
        Request.Builder().url(url("/watch/sessions")).post(payload.toRequestBody(JSON_MEDIA))
    }

    // ---- actions ----

    suspend fun approve(sessionId: String, requestId: String, scope: String = "once") =
        execute {
            val payload = json.encodeToString(
                kotlinx.serialization.json.JsonObject.serializer(),
                kotlinx.serialization.json.buildJsonObject {
                    put("scope", kotlinx.serialization.json.JsonPrimitive(scope))
                },
            )
            Request.Builder()
                .url(url("/watch/approvals/$sessionId/$requestId/approve"))
                .post(payload.toRequestBody(JSON_MEDIA))
        }

    suspend fun deny(sessionId: String, requestId: String, reason: String = "denied from watch") =
        execute {
            val payload = json.encodeToString(
                kotlinx.serialization.json.JsonObject.serializer(),
                kotlinx.serialization.json.buildJsonObject {
                    put("reason", kotlinx.serialization.json.JsonPrimitive(reason))
                },
            )
            Request.Builder()
                .url(url("/watch/approvals/$sessionId/$requestId/deny"))
                .post(payload.toRequestBody(JSON_MEDIA))
        }

    /**
     * Stops the turn currently running in a session (mockup 05).
     *
     * The watch cannot know a turn id, so it asks the server to stop whatever is
     * active; the server answers 409 when nothing is running rather than
     * pretending the request did something.
     */
    suspend fun stopSession(sessionId: String) = execute {
        Request.Builder()
            .url(url("/watch/sessions/$sessionId/stop"))
            .post("".toRequestBody(JSON_MEDIA))
    }

    /**
     * Fetches spoken audio for a session's latest reply (B7).
     *
     * Returns raw WAV bytes, so it needs its own path rather than [call] (which
     * string-decodes the body to JSON). The node derives the text using its own
     * wrist-shortened summary, so the watch does not re-implement that shaping.
     *
     * A session with nothing to read back answers 404, which surfaces here as a
     * normal [WatchApiException] the UI can explain instead of playing silence.
     */
    suspend fun synthesizeSession(sessionId: String): ByteArray = rawBytes {
        val payload = json.encodeToString(
            kotlinx.serialization.json.JsonObject.serializer(),
            kotlinx.serialization.json.buildJsonObject {
                put("session_id", kotlinx.serialization.json.JsonPrimitive(sessionId))
            },
        )
        Request.Builder().url(url("/watch/tts")).post(payload.toRequestBody(JSON_MEDIA))
    }

    suspend fun sendCommand(sessionId: String, text: String, source: String = "text"): CommandAccepted =
        call {
            val payload = json.encodeToString(CommandRequest.serializer(), CommandRequest(sessionId, text, source))
            Request.Builder().url(url("/watch/command")).post(payload.toRequestBody(JSON_MEDIA))
        }

    /**
     * Uploads recorded audio for server-side transcription.
     *
     * This is the main dictation path, not a fallback: a device without a Google
     * or vendor speech service exposes no `RecognitionService` at all, so the
     * on-device recogniser cannot be relied on.
     */
    suspend fun transcribe(audio: File): TranscribeResult = call {        val body = MultipartBody.Builder()
            .setType(MultipartBody.FORM)
            .addFormDataPart(
                "audio",
                audio.name,
                audio.asRequestBody("audio/wav".toMediaType()),
            )
            .build()
        Request.Builder().url(url("/watch/asr")).post(body)
    }

    companion object {
        /**
         * Default node address when nothing is configured.
         *
         * `10.0.2.2` is the emulator's alias for the host machine's loopback, so a
         * watch being developed against a local `godex serve` works with no setup.
         *
         * The `/api` suffix is required: the node's web layer strips a leading
         * `/api` before delegating to the API handler, and without it the request
         * falls through to the web UI and returns SPA HTML instead of JSON
         * (verified end to end — `/meta` returns HTML, `/api/meta` returns JSON).
         */
        const val DEFAULT_EMULATOR_URL = "http://10.0.2.2:8080/api"

        /**
         * Appends the required `/api` prefix when the user typed a bare host
         * (e.g. `http://192.168.1.10:8080`), so a natural-looking value still
         * reaches the API instead of the web UI.
         */
        fun normalizeBaseUrl(raw: String): String {
            val trimmed = raw.trim().trimEnd('/')
            if (trimmed.isEmpty()) return ""
            // Already explicit (either /api or some other path the user chose).
            val withoutScheme = trimmed.substringAfter("://", trimmed)
            if (withoutScheme.contains('/')) return trimmed
            return "$trimmed/api"
        }

        private val JSON_MEDIA = "application/json; charset=utf-8".toMediaType()

        fun defaultJson(): Json = Json {
            ignoreUnknownKeys = true
            explicitNulls = false
            coerceInputValues = true
        }

        fun defaultClient(): OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(8, TimeUnit.SECONDS)
            .readTimeout(20, TimeUnit.SECONDS)
            .writeTimeout(20, TimeUnit.SECONDS)
            // A watch link drops often; a single retry fights transient blips
            // without turning a failed action into a battery-draining storm.
            .retryOnConnectionFailure(true)
            .build()
    }
}
