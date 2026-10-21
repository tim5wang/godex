package com.godex.wear.ui

import android.app.Application
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.godex.wear.data.Credentials
import com.godex.wear.data.WatchApi
import com.godex.wear.data.WatchApiException
import com.godex.wear.data.WatchApproval
import com.godex.wear.data.WatchCrossDeviceSession
import com.godex.wear.data.WatchDevice
import com.godex.wear.data.WatchNode
import com.godex.wear.data.WatchSession
import com.godex.wear.data.WatchSessionDetail
import com.godex.wear.data.WatchStore
import com.godex.wear.data.WatchPreferences
import com.godex.wear.notify.WearNotifier
import com.godex.wear.voice.VoiceCapture
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

/** One-shot user-visible message (snackbar / inline notice). */
data class Notice(val text: String, val isError: Boolean = false)

/**
 * Single source of truth for the watch UI.
 *
 * A watch has one screen and one user, so a single state holder is simpler and
 * less error-prone than per-screen ViewModels that would each re-fetch the same
 * session list.
 */
class WatchViewModel(app: Application) : AndroidViewModel(app) {

    private val store = WatchStore(app)
    val api = WatchApi()

    /** Recording + transcription (main dictation path; see VoiceCapture). */
    val voice = VoiceCapture(app)

    /** True while the user is holding the talk button. */
    private val _recording = MutableStateFlow(false)
    val recording: StateFlow<Boolean> = _recording.asStateFlow()

    data class UiState(
        val loading: Boolean = false,
        val credentials: Credentials? = null,
        val nodes: List<WatchNode> = emptyList(),
        val sessions: List<WatchSession> = emptyList(),
        val approvals: List<WatchApproval> = emptyList(),
        val detail: WatchSessionDetail? = null,
        val currentSessionId: String = "",
        val targetDeviceId: String = "",
        val error: String? = null,
        /** Sessions from every node this center can see (mockup 14). */
        val crossDevice: List<WatchCrossDeviceSession> = emptyList(),
        /** Session ids the user pinned (mockup 13). Order is local, not server. */
        val pinned: Set<String> = emptySet(),
        /** Settings switches (mockup 11). */
        val preferences: WatchPreferences = WatchPreferences(),
    )

    private val _state = MutableStateFlow(UiState(loading = true))
    val state: StateFlow<UiState> = _state.asStateFlow()

    private val _notice = MutableStateFlow<Notice?>(null)
    val notice: StateFlow<Notice?> = _notice.asStateFlow()

    /**
     * Background refresh loop.
     *
     * Without this the watch only updated when the user tapped refresh, which
     * made the notification (mockup 08) and the ongoing activity (mockup 12)
     * unreachable: neither can fire if nothing ever re-reads the session state.
     *
     * Preferring [startPolling]/[stopPolling] over a loop started in `init` is
     * deliberate: polling must stop while the watch is off-wrist, and the caller
     * (which knows the screen visibility) owns that decision.
     */
    private var pollingJob: Job? = null

    /**
     * Last observed running state of the selected session, used to detect the
     * running→idle edge that triggers a "task finished" notification.
     */
    private var lastRunningKey: String? = null

    init {
        viewModelScope.launch { bootstrap() }
    }

    /**
     * Loads persisted credentials and performs the first refresh.
     *
     * A 401 here means the credential was revoked on the node side (or the node
     * was wiped); in that case local state is cleared so the UI falls back to
     * pairing rather than showing a dead remote control.
     */
    private suspend fun bootstrap() {
        val creds = store.credentials()
        api.baseUrl = creds.baseUrl.ifEmpty { WatchApi.DEFAULT_EMULATOR_URL }
        api.token = creds.token
        _state.value = _state.value.copy(
            credentials = creds,
            loading = false,
            preferences = store.preferences(),
        )

        if (!creds.isUsable) return
        refresh()
    }

    /**
     * Refresh interval, derived from what is actually happening.
     *
     * A watch that polls at a fixed fast rate burns battery saying nothing has
     * changed; one that polls slowly misses the ending of a task the user is
     * waiting on. So the interval tightens only while something is running, and
     * the power-saving switch widens both cases.
     */
    private fun pollIntervalMillis(): Long {
        val running = _state.value.detail?.running == true
        val saving = _state.value.preferences.powerSaving
        return when {
            running && saving -> 20_000L
            running -> 10_000L
            saving -> 90_000L
            else -> 30_000L
        }
    }

    /** Starts the periodic refresh; repeated calls replace the previous loop. */
    fun startPolling() {
        pollingJob?.cancel()
        if (_state.value.credentials?.isUsable != true) return
        pollingJob = viewModelScope.launch {
            while (true) {
                delay(pollIntervalMillis())
                // Skip the round-trip while a refresh is already in flight; the
                // loading flag is the existing "a refresh is running" signal.
                if (_state.value.loading) continue
                refresh()
            }
        }
    }

    /**
     * Stops the loop.
     *
     * Called when the UI is no longer visible. The last known state is kept so
     * returning to the screen shows something immediately instead of a blank
     * screen while the first refresh is in flight.
     */
    fun stopPolling() {
        pollingJob?.cancel()
        pollingJob = null
    }

    fun setNotifyOnComplete(value: Boolean) {
        viewModelScope.launch {
            store.setNotifyOnComplete(value)
            _state.value = _state.value.copy(preferences = store.preferences())
        }
    }

    fun setPowerSaving(value: Boolean) {
        viewModelScope.launch {
            store.setPowerSaving(value)
            _state.value = _state.value.copy(preferences = store.preferences())
            // Re-arm the loop so the new interval takes effect now rather than
            // after the (possibly 90s) wait the old one had already started.
            startPolling()
        }
    }

    fun setAmbient(value: Boolean) {
        viewModelScope.launch {
            store.setAmbient(value)
            _state.value = _state.value.copy(preferences = store.preferences())
            // Turning the live card off must remove an already-posted one.
            WearNotifier.updateOngoing(
                getApplication(),
                if (value) _state.value.detail else null,
                sessionTitleFor(_state.value.currentSessionId),
            )
        }
    }

    fun dismissNotice() {
        _notice.value = null
    }

    fun setServerUrl(url: String) {
        viewModelScope.launch {
            store.saveServerUrl(url)
            api.baseUrl = com.godex.wear.data.WatchApi.normalizeBaseUrl(url)
            _state.value = _state.value.copy(
                credentials = _state.value.credentials?.copy(baseUrl = api.baseUrl),
            )
        }
    }

    /** Called after a successful pairing so the client picks up the credential. */
    fun onPaired(device: WatchDevice, token: String) {
        viewModelScope.launch {
            // Capture the address that actually paired. Falling back to the
            // default keeps the credential usable even if baseUrl was never set.
            val pairedUrl = api.baseUrl.ifEmpty { WatchApi.DEFAULT_EMULATOR_URL }
            store.savePairedDevice(device, token, pairedUrl)
            api.baseUrl = com.godex.wear.data.WatchApi.normalizeBaseUrl(pairedUrl)
            api.token = token
            _state.value = _state.value.copy(
                credentials = store.credentials(),
                error = null,
            )
            refresh()
        }
    }

    fun refresh() {
        viewModelScope.launch {
            _state.value = _state.value.copy(loading = true, error = null)
            try {
                // The overview carries node + session + approval counts, so the
                // home screen needs exactly one round-trip.
                val overview = api.overview()
                val nodes = api.nodes()
                val sessions = api.sessions()
                val approvals = api.approvals()
                val pinned = store.pinnedSessions().toSet()
                // Signalled separately so a cross-node read failure never blanks
                // the local list: the home screen must survive a dead peer.
                val crossDevice = runCatching { api.crossDeviceSessions() }.getOrDefault(emptyList())
                val current = resolveCurrentSession(sessions)
                _state.value = _state.value.copy(
                    loading = false,
                    nodes = nodes,
                    sessions = sessions,
                    approvals = approvals,
                    crossDevice = crossDevice,
                    pinned = pinned,
                    currentSessionId = current,
                    targetDeviceId = overview.devices.firstOrNull()?.id
                        ?: _state.value.targetDeviceId,
                    error = null,
                )
                if (current.isNotEmpty()) loadDetail(current)
                // Surface the queue on the wrist: an approval is the one event that
                // actually needs the user, so it is also the one that interrupts
                // (mockup 08). Driven from the same refresh that fills the screens,
                // so the notification and the in-app queue cannot disagree.
                syncWearSurfaces(approvals, current)
            } catch (err: WatchApiException) {
                handleApiError(err)
            } catch (err: Exception) {
                _state.value = _state.value.copy(loading = false, error = err.message ?: "unreachable")
            }
        }
    }

    /**
     * Picks the session the remote control points at: the remembered one for this
     * node when still present, else the most recent.
     */
    private suspend fun resolveCurrentSession(sessions: List<WatchSession>): String {
        val remembered = store.sessionForDevice(api.baseUrl)
        if (remembered.isNotEmpty() && sessions.any { it.sessionId == remembered }) return remembered
        return sessions.firstOrNull()?.sessionId.orEmpty()
    }

    fun selectSession(sessionId: String) {
        viewModelScope.launch {
            store.rememberSession(api.baseUrl, sessionId)
            _state.value = _state.value.copy(currentSessionId = sessionId)
            loadDetail(sessionId)
        }
    }

    fun setTargetDevice(deviceId: String) {
        viewModelScope.launch {
            store.setTargetDevice(deviceId)
            _state.value = _state.value.copy(targetDeviceId = deviceId)
        }
    }

    /**
     * Pins or unpins a session (mockup 13).
     *
     * Pinning is local to the watch: it is a personal shortcut, not a change to
     * the session itself, so it never reaches the node.
     */
    fun togglePin(sessionId: String) {
        if (sessionId.isEmpty()) return
        viewModelScope.launch {
            store.togglePinned(sessionId)
            _state.value = _state.value.copy(pinned = store.pinnedSessions().toSet())
        }
    }

    /**
     * Stops the turn running in the selected session (mockup 05).
     *
     * A 409 means nothing was running, which is reported as information rather
     * than an error: the user's goal ("stop it") is already satisfied.
     */
    fun stopCurrentSession() {
        val sessionId = _state.value.currentSessionId
        if (sessionId.isEmpty()) {
            _notice.value = Notice("先选一个会话", isError = true)
            return
        }
        viewModelScope.launch {
            try {
                api.stopSession(sessionId)
                _notice.value = Notice("已停止")
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) {
                    handleApiError(err)
                    return@launch
                }
                val msg = if (err.statusCode == 409) "没有正在运行的任务" else err.message
                _notice.value = Notice(msg, isError = err.statusCode != 409)
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: "停止失败", isError = true)
                return@launch
            }
            refresh()
        }
    }

    private suspend fun loadDetail(sessionId: String) {
        try {
            val detail = api.sessionDetail(sessionId)
            val previous = _state.value.detail
            noteRunningState(sessionId, detail.running)
            _state.value = _state.value.copy(detail = detail)
            // The ongoing-activity card (mockup 12) is derived from the same
            // detail the detail screen renders, for the same reason as the
            // notification: one source, so the surfaces cannot drift apart.
            WearNotifier.updateOngoing(
                getApplication(),
                if (_state.value.preferences.ambient) detail else null,
                sessionTitleFor(sessionId),
            )
            notifyIfJustFinished(sessionId, previous, detail)
        } catch (err: WatchApiException) {
            // A vanished session is not worth an error banner: the list refresh
            // will drop it and the user just sees the newer list.
            if (!err.isAuthFailure) {
                _state.value = _state.value.copy(detail = null)
            } else {
                handleApiError(err)
            }
        }
    }

    /**
     * Posts the "task finished" notification (mockup 08) on the running→idle
     * edge.
     *
     * Edge-triggered rather than level-triggered: without the transition check the
     * same finished task would re-notify on every poll, which on a wrist means a
     * buzz every 20 seconds forever.
     */
    private fun notifyIfJustFinished(
        sessionId: String,
        previous: WatchSessionDetail?,
        current: WatchSessionDetail,
    ) {
        // Only compare the same session, and only once per transition. The key
        // records which session's transition was already reported.
        if (previous?.sessionId != sessionId) return
        if (!previous.running || current.running) return
        if (lastRunningKey == sessionId) return
        lastRunningKey = sessionId
        if (!_state.value.preferences.notifyOnComplete) return
        WearNotifier.notifyComplete(
            getApplication(),
            sessionTitleFor(sessionId).ifBlank { current.title },
            current.sessionId,
        )
    }

    /**
     * Clears the "already reported" marker once the session runs again.
     *
     * Without this a second run of the same session would be silent: the
     * transition guard would still hold the session id from the first run.
     */
    private fun noteRunningState(sessionId: String, running: Boolean) {
        if (running && lastRunningKey == sessionId) lastRunningKey = null
    }

    /**
     * Mirrors the approval queue onto the notification surface.
     *
     * Only the first actionable item is posted: a watch shade is tiny and the
     * in-app queue (mockup 15) is the complete view, so several near-identical
     * alerts would only be noise. An empty queue clears the notification, because
     * a stale "approve?" alert offering buttons for an already-answered request is
     * actively harmful.
     */
    private fun syncWearSurfaces(approvals: List<WatchApproval>, currentSessionId: String) {
        val app = getApplication<Application>()
        val head = approvals.firstOrNull { it.isPermission }
        if (head == null) {
            WearNotifier.clearApproval(app)
            return
        }
        val sessionId = head.sessionId.orEmpty().ifEmpty { currentSessionId }
        WearNotifier.notifyApproval(app, head, sessionTitleFor(sessionId))
    }

    private fun sessionTitleFor(sessionId: String): String =
        _state.value.sessions.firstOrNull { it.sessionId == sessionId }?.title.orEmpty()

    /** Sends a dictation or typed instruction to the currently selected session. */
    fun sendCommand(text: String, source: String = "text") {
        val sessionId = _state.value.currentSessionId
        if (sessionId.isEmpty()) {
            _notice.value = Notice("先选一个会话", isError = true)
            return
        }
        if (text.isBlank()) return

        viewModelScope.launch {
            try {
                val accepted = api.sendCommand(sessionId, text.trim(), source)
                val suffix = if (accepted.pendingApproval) "（需审批）" else "（已排队）"
                _notice.value = Notice("已发送$suffix")
                loadDetail(sessionId)
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) handleApiError(err) else _notice.value = Notice(err.message, isError = true)
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: "发送失败", isError = true)
            }
        }
    }

    fun approve(item: WatchApproval, scope: String = "once") {
        val sessionId = item.sessionId ?: return
        val requestId = item.requestId ?: return
        viewModelScope.launch {
            try {
                // A wrist tap grants the narrowest authority by default; the user
                // can re-grant from a fuller client if they want more.
                api.approve(sessionId, requestId, scope)
                _notice.value = Notice(if (scope == "session") "本会话已全部允许" else "已批准")
                // Leaving the detail screen is the caller's job: the request may
                // already be gone from the next list read, and a detail screen
                // for a resolved request would keep offering dead buttons.
                refresh()
            } catch (err: WatchApiException) {
                // 409 means someone (or another client) already resolved it. That
                // is a normal race on a shared queue, so it gets a plain notice.
                val msg = if (err.statusCode == 409) "已被其他端处理" else err.message
                _notice.value = Notice(msg, isError = err.statusCode != 409)
                refresh()
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: "批准失败", isError = true)
            }
        }
    }

    fun deny(item: WatchApproval) {
        val sessionId = item.sessionId ?: return
        val requestId = item.requestId ?: return
        viewModelScope.launch {
            try {
                api.deny(sessionId, requestId)
                _notice.value = Notice("已拒绝")
                refresh()
            } catch (err: WatchApiException) {
                _notice.value = Notice(err.message, isError = true)
                refresh()
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: "拒绝失败", isError = true)
            }
        }
    }

    /**
     * Approval shown on the single-approval screen (mockup 06).
     *
     * Stored as a key, not the item: the refresh loop re-reads the queue, so the
     * screen re-resolves the request against the latest list. Holding the item
     * would render a stale snapshot and could keep showing buttons for a request
     * that another client already resolved.
     */
    private val _openApprovalKey = MutableStateFlow<String?>(null)
    val openApprovalKey: StateFlow<String?> = _openApprovalKey.asStateFlow()

    fun openApproval(item: WatchApproval) {
        _openApprovalKey.value = item.key
    }

    fun closeApproval() {
        _openApprovalKey.value = null
    }
    /**
     * Begins push-to-talk capture.
     *
     * Returns false when the microphone is unavailable so the UI can ask for the
     * permission instead of pretending to listen.
     */
    fun startRecording(): Boolean {
        if (!voice.hasMicrophonePermission()) {
            _notice.value = Notice("需要麦克风权限", isError = true)
            return false
        }
        val started = voice.start()
        if (!started) {
            _notice.value = Notice("无法开始录音", isError = true)
            return false
        }
        _recording.value = true
        return true
    }

    /** Stops capture, uploads the clip, and sends the transcript as a command. */
    fun stopRecordingAndSend() {
        if (!_recording.value) return
        _recording.value = false
        viewModelScope.launch {
            val file = try {
                voice.stop()
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: "录音失败", isError = true)
                return@launch
            }
            if (file == null) {
                _notice.value = Notice("没有听到声音", isError = true)
                return@launch
            }
            try {
                val result = api.transcribe(file)
                if (result.text.isBlank()) {
                    _notice.value = Notice("没有识别出内容", isError = true)
                } else {
                    // Provenance stays "voice" end-to-end so the transcript can
                    // be analysed separately from typed input later.
                    sendCommand(result.text, source = "voice")
                }
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) {
                    handleApiError(err)
                } else {
                    _notice.value = Notice(err.message, isError = true)
                }
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: "转写失败", isError = true)
            } finally {
                file.delete()
            }
        }
    }

    fun cancelRecording() {
        if (!_recording.value) return
        _recording.value = false
        voice.cancel()
    }

    fun unpair() {        viewModelScope.launch {
            store.clearCredentials()
            api.token = ""
            _state.value = UiState(loading = false, credentials = store.credentials())
        }
    }

    /** Revocation and expiry both surface as auth failures; treat them the same. */
    private fun handleApiError(err: WatchApiException) {
        if (err.isAuthFailure) {
            viewModelScope.launch {
                store.clearCredentials()
                api.token = ""
                _state.value = UiState(loading = false, credentials = store.credentials())
                _notice.value = Notice("配对已失效，请重新配对", isError = true)
            }
            return
        }
        _state.value = _state.value.copy(loading = false, error = err.message)
    }
}
