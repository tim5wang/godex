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
import com.godex.wear.data.WatchHistoryMessage
import com.godex.wear.data.WatchNode
import com.godex.wear.data.WatchSession
import com.godex.wear.data.WatchSessionDetail
import com.godex.wear.data.WatchStore
import com.godex.wear.data.WatchTemplate
import com.godex.wear.data.WatchPreferences
import com.godex.wear.R
import com.godex.wear.notify.WearNotifier
import com.godex.wear.sync.WatchSync
import com.godex.wear.sync.announceFinishedSessions
import com.godex.wear.voice.VoiceCapture
import com.godex.wear.voice.VoicePlayback
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
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

    private companion object {
        /**
         * Node id the node itself uses for sessions it serves locally.
         *
         * Aliased from [WatchStore] rather than re-declared: the background sync
         * worker needs the same value, and two independent literals would eventually
         * drift — a drift that silently turns a local request into a forwarded one
         * (or the reverse) without any test noticing.
         */
        const val LOCAL_DEVICE_ID = WatchStore.LOCAL_DEVICE_ID
    }

    private val store = WatchStore(app)
    val api = WatchApi()

    /** Recording + transcription (main dictation path; see VoiceCapture). */
    val voice = VoiceCapture(app)

    /** Speech playback for reading a reply back (B7; see VoicePlayback). */
    private val playback = VoicePlayback()

    /** True while the user is holding the talk button. */
    private val _recording = MutableStateFlow(false)
    val recording: StateFlow<Boolean> = _recording.asStateFlow()

    /** True while a reply is being spoken, so the button can offer to stop it. */
    private val _speaking = MutableStateFlow(false)
    val speaking: StateFlow<Boolean> = _speaking.asStateFlow()

    /**
     * Recent conversation, loaded on demand when the user opens history.
     *
     * Not part of [UiState]: that object is replaced wholesale on every
     * background poll, and folding the conversation into it would either get
     * cleared by each refresh or force the poll to carry it. History only changes
     * when a turn completes, so it is fetched when asked for.
     */
    private val _history = MutableStateFlow<List<WatchHistoryMessage>>(emptyList())
    val history: StateFlow<List<WatchHistoryMessage>> = _history.asStateFlow()

    private val _historyLoading = MutableStateFlow(false)
    val historyLoading: StateFlow<Boolean> = _historyLoading.asStateFlow()

    /**
     * Resolves a UI string.
     *
     * Not a composable, so stringResource is unavailable; the application context
     * carries the same locale configuration, which keeps a notice in whatever
     * language the watch is set to.
     */
    private fun string(resId: Int, vararg args: Any): String =
        getApplication<Application>().getString(resId, *args)

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
        /** Presets offered when creating a conversation from the wrist. */
        val templates: List<WatchTemplate> = emptyList(),
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
            // Restore the device the user last chose. Without this the selection
            // was written to DataStore and never read back, so every cold start
            // silently re-pointed the remote control at the first device.
            targetDeviceId = store.targetDeviceFlow.first(),
        )

        if (!creds.isUsable) {
            // Not paired: a background pull would have nothing to read. Cancelling
            // here (rather than only on unpair) also cleans up after a credential
            // that was revoked while the app was closed.
            WatchSync.cancel(getApplication())
            return
        }
        ensureBackgroundSync()
        refresh()
    }

    /**
     * Keeps the background pull in step with the pairing state.
     *
     * Idempotent by construction ([WatchSync.schedule] keeps any existing work), so
     * it is safe to call on every start and after every pairing rather than
     * tracking whether it was already scheduled.
     */
    private fun ensureBackgroundSync() {
        WatchSync.schedule(getApplication())
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
            // A freshly paired watch is exactly when the background pull matters:
            // approving from the wrist is only useful if the request reaches it
            // while the app is closed.
            ensureBackgroundSync()
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
                // A node's own entry is renamed to the local sentinel here, once, so
                // every downstream decision sees a single representation. The device
                // list carries real node ids while the session list labels this node
                // "local"; keeping both spellings would make picking your own node
                // off the list indistinguishable from picking a peer.
                val nodes = normalizeSelf(api.nodes())
                val devices = normalizeSelf(overview.devices)
                val pinned = store.pinnedSessions().toSet()
                // Signalled separately so a cross-node read failure never blanks
                // the local list: the home screen must survive a dead peer.
                val crossDevice = runCatching { api.crossDeviceSessions() }.getOrDefault(emptyList())
                // Reconcile the target device BEFORE reading sessions: the two are
                // coupled now that a wrist can drive several nodes. Reading first
                // would fetch the list for the previously selected device, so the
                // selection would change while the contents stayed behind.
                val target = reconcileTargetDevice(
                    devices = devices,
                    current = _state.value.targetDeviceId,
                )
                val remoteNode = remoteNodeIdFor(target)
                // The local list is always read: it is the richer payload (turn
                // identity drives the completion notification).
                val localSessions = api.sessions()
                // A remote read failure must NOT fall back to this node's list.
                //
                // It used to, on the theory that showing something beats a blank
                // screen. That was wrong in the one case it matters: with "mac"
                // selected but the mac unreachable, the wrist showed the *paired
                // node's* conversations under the mac's name, which reads as "the
                // device switch did nothing". An empty list plus a visible error
                // says what actually happened, and it cannot be mistaken for the
                // wrong machine's data.
                var remoteError: String? = null
                val sessions: List<WatchSession>
                val approvals: List<WatchApproval>
                if (remoteNode.isEmpty()) {
                    sessions = localSessions
                    approvals = api.approvals()
                } else {
                    val fetched = runCatching { api.sessions(nodeId = remoteNode) }
                    sessions = fetched.getOrElse { err ->
                        remoteError = err.message ?: string(R.string.vm_remote_unreachable)
                        emptyList()
                    }
                    approvals = runCatching { api.approvals(nodeId = remoteNode) }
                        .getOrElse { emptyList() }
                }
                _state.value = _state.value.copy(targetDeviceId = target)
                val current = resolveCurrentSession(sessions)
                _state.value = _state.value.copy(
                    loading = false,
                    nodes = nodes,
                    sessions = sessions,
                    approvals = approvals,
                    crossDevice = crossDevice,
                    pinned = pinned,
                    currentSessionId = current,
                    targetDeviceId = target,
                    error = remoteError,
                )
                if (current.isNotEmpty()) loadDetail(current)
                // Surface the queue on the wrist: an approval is the one event that
                // actually needs the user, so it is also the one that interrupts
                // (mockup 08). Driven from the same refresh that fills the screens,
                // so the notification and the in-app queue cannot disagree.
                syncWearSurfaces(approvals, current)
                // "Task finished" (mockup 08) is detected from the same session list
                // this refresh just read, via the helper shared with the background
                // worker. Both sides compare against one persisted running set, so a
                // task that ends while the app is open is not announced again by the
                // next background run.
                announceFinishedSessions(
                    context = getApplication(),
                    sessions = sessions,
                    store = store,
                    notifyEnabled = _state.value.preferences.notifyOnComplete,
                )
            } catch (err: WatchApiException) {
                handleApiError(err)
            } catch (err: Exception) {
                _state.value = _state.value.copy(loading = false, error = err.message ?: "unreachable")
            }
        }
    }

    /**
     * Picks the session the remote control points at: the remembered one for the
     * selected device when still present, else the most recent on that device.
     *
     * Remembered per device rather than per server URL. With multi-node control a
     * single base URL now spans several devices, so keying by URL would let a
     * session remembered on node B be "restored" while the wrist is pointed at
     * node C, where it does not exist.
     */
    private suspend fun resolveCurrentSession(sessions: List<WatchSession>): String {
        val remembered = store.sessionForDevice(deviceKey())
        if (remembered.isNotEmpty() && sessions.any { it.sessionId == remembered }) return remembered
        return sessions.firstOrNull()?.sessionId.orEmpty()
    }

    /** Stable key for "which device owns the session I am looking at". */
    private fun deviceKey(): String = remoteNodeId().ifEmpty { LOCAL_DEVICE_ID }

    fun selectSession(sessionId: String) {
        viewModelScope.launch {
            store.rememberSession(deviceKey(), sessionId)
            _state.value = _state.value.copy(currentSessionId = sessionId)
            loadDetail(sessionId)
        }
    }

    /**
     * Opens a session picked from the cross-device list.
     *
     * Moves the remote control to that session's node first. A session on another
     * device is only reachable by naming that device on every request, so without
     * this the tap selected an id the paired node had never heard of, and the next
     * refresh replaced the list with the node actually being driven — the switch
     * looked like it did nothing.
     *
     * The target is persisted before the refetch so a crash mid-switch still comes
     * back pointed at the device the user chose.
     */
    fun selectCrossDeviceSession(nodeId: String, sessionId: String) {
        val target = nodeId.ifBlank { LOCAL_DEVICE_ID }
        viewModelScope.launch {
            store.setTargetDevice(target)
            store.rememberSession(target, sessionId)
            _state.value = _state.value.copy(
                targetDeviceId = target,
                currentSessionId = sessionId,
                detail = null,
            )
            // Refetch before loading the detail: the detail request carries the new
            // node id, and the list must reflect the new device before the UI can
            // render a coherent pair of screens.
            refresh()
            loadDetail(sessionId)
        }
    }

    fun setTargetDevice(deviceId: String) {
        viewModelScope.launch {
            store.setTargetDevice(deviceId)
            // Clear before refetching: the session and detail on screen belong to the
            // old device, and leaving them visible would let the user send a command
            // to a session that is not on the newly selected device.
            _state.value = _state.value.copy(
                targetDeviceId = deviceId,
                currentSessionId = "",
                detail = null,
            )
            // Refetch rather than filtering the list already in hand: refresh now
            // reads the selected device's sessions from the node, which is the only
            // way to see a peer's sessions that were not in the previous payload.
            refresh()
        }
    }

    /**
     * Maps this node's own entry onto the local sentinel.
     *
     * The node marks its own registry entry with `self`; the session list calls the
     * same machine `local`. Collapsing them here means one representation flows
     * through target selection, request routing and the device screen, so "the
     * computer I am paired with" can never be mistaken for a remote peer.
     */
    private fun normalizeSelf(nodes: List<WatchNode>): List<WatchNode> =
        nodes.map { if (it.self) it.copy(id = LOCAL_DEVICE_ID) else it }

    /**
     * Keeps the stored target device honest across refreshes.
     *
     * The previous code unconditionally reset the selection to the first device on
     * every refresh, which made the "which device am I driving?" screen
     * decorative: picking a device survived only until the next poll. Rules kept
     * deliberately narrow:
     *
     *  * a selection that is still present is preserved, always;
     *  * an empty selection defaults to THIS node, not to `devices.first()`;
     *  * a selection that disappeared (device removed) falls back the same way.
     *
     * Defaulting to the first entry looked natural but is unsafe the moment the
     * list can contain other machines: whichever node happens to sort first would
     * silently become the destination for every instruction, and the user's first
     * command after pairing could run on a different computer than the one they
     * paired with. The paired node is the only defensible default — it is the one
     * device the user has explicitly authorised.
     */
    private fun reconcileTargetDevice(devices: List<WatchNode>, current: String): String {
        if (current == LOCAL_DEVICE_ID) return current
        if (devices.any { it.id == current }) return current
        return LOCAL_DEVICE_ID
    }

    /**
     * Sessions that live on one device.
     *
     * Sourced from the cross-device list because it is the only surface carrying
     * `node_id` per session; the plain session list only ever describes the node
     * this watch is paired with. The local device keeps using [localSessions],
     * which is the richer payload (it carries turn identity used for notifications)
     * and is also the only list that works against an older node without the
     * cross-device endpoint.
     *
     * Takes its inputs as parameters rather than reading state so callers can use
     * the values they just fetched. Reading `_state` here would race with the
     * assignment that follows, and a stale device list would silently produce an
     * empty session list for a device that does have sessions.
     */
    private fun sessionsForTarget(
        deviceId: String,
        localSessions: List<WatchSession>,
        crossDevice: List<WatchCrossDeviceSession>,
    ): List<WatchSession> {
        if (deviceId.isBlank() || deviceId == LOCAL_DEVICE_ID) return localSessions
        return crossDevice
            .filter { it.nodeId == deviceId }
            .map {
                WatchSession(
                    sessionId = it.sessionId,
                    title = it.title,
                    running = it.running,
                    parentSessionId = it.parentSessionId,
                    lastActivityAt = it.lastActivityAt,
                )
            }
    }

    /**
     * The node id to attach to remote-control requests.
     *
     * Empty means "the node this watch is paired with", which is also what the
     * node itself uses for its own sessions. Sending the id explicitly for the
     * local case would work too, but keeping it empty means an older node that
     * does not know `node_id` still serves the request instead of erroring, so a
     * watch stays usable against a node that was not upgraded.
     */
    private fun remoteNodeId(): String = remoteNodeIdFor(_state.value.targetDeviceId)

    /**
     * Node id to attach to requests, for a target the caller already resolved.
     *
     * Separate from [remoteNodeId] because refresh computes the target from the
     * freshly fetched device list before it is committed to state; reading state
     * there would use the previous target and send the read to the wrong device.
     */
    private fun remoteNodeIdFor(target: String): String =
        if (target.isBlank() || target == LOCAL_DEVICE_ID) "" else target

    /**
     * Loads the presets offered when creating a conversation on the wrist.
     *
     * Fetched lazily when the create sheet opens rather than on every refresh:
     * these are static presets, and a watch should not spend radio time on a list
     * nobody has asked to see.
     */
    fun loadTemplates() {
        if (_state.value.templates.isNotEmpty()) return
        viewModelScope.launch {
            runCatching { api.templates() }
                .onSuccess { list ->
                    _state.value = _state.value.copy(templates = list)
                }
                .onFailure { err ->
                    // Not surfaced as an error: the create sheet still works with the
                    // default preset, and a failed catalogue fetch is not something
                    // the user can act on.
                    if (err is WatchApiException && err.isAuthFailure) handleApiError(err)
                }
        }
    }

    /**
     * Creates a conversation from the wrist and points the remote at it.
     *
     * Selecting it immediately is the point: the user tapped "新建" to talk to
     * something, so leaving the remote on the previous session would make the
     * next utterance go to the wrong conversation.
     */
    fun createSession(templateId: String = "") {
        viewModelScope.launch {
            try {
                val created = api.createSession(templateId = templateId)
                store.rememberSession(api.baseUrl, created.sessionId)
                _state.value = _state.value.copy(
                    currentSessionId = created.sessionId,
                    detail = null,
                )
                _notice.value = Notice(string(R.string.vm_created))
                refresh()
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) handleApiError(err)
                else _notice.value = Notice(err.message, isError = true)
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_create_failed), isError = true)
            }
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
            _notice.value = Notice(string(R.string.vm_select_session_first), isError = true)
            return
        }
        viewModelScope.launch {
            try {
                api.stopSession(sessionId, remoteNodeId())
                _notice.value = Notice(string(R.string.vm_stopped))
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) {
                    handleApiError(err)
                    return@launch
                }
                val msg = if (err.statusCode == 409) string(R.string.vm_nothing_running)
                else err.message
                _notice.value = Notice(msg, isError = err.statusCode != 409)
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_stop_failed), isError = true)
                return@launch
            }
            refresh()
        }
    }

    private suspend fun loadDetail(sessionId: String) {
        try {
            val detail = api.sessionDetail(sessionId, remoteNodeId())
            _state.value = _state.value.copy(detail = detail)
            // The ongoing-activity card (mockup 12) is derived from the same
            // detail the detail screen renders, for the same reason as the
            // notification: one source, so the surfaces cannot drift apart.
            WearNotifier.updateOngoing(
                getApplication(),
                if (_state.value.preferences.ambient) detail else null,
                sessionTitleFor(sessionId),
            )
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

    /**
     * Sends a dictation or typed instruction to the currently selected session.
     *
     * [onSent] runs only after the node accepted the instruction. The caller uses
     * it to leave the compose screen; calling it on failure would dismiss the
     * screen and take the error with it, leaving the user with no way to retry
     * the text they just spoke or typed.
     */
    fun sendCommand(text: String, source: String = "text", onSent: (() -> Unit)? = null) {
        val sessionId = _state.value.currentSessionId
        if (sessionId.isEmpty()) {
            _notice.value = Notice(string(R.string.vm_select_session_first), isError = true)
            return
        }
        if (text.isBlank()) return

        viewModelScope.launch {
            try {
                val accepted = api.sendCommand(sessionId, text.trim(), source, remoteNodeId())
                val suffix = if (accepted.pendingApproval) string(R.string.vm_needs_approval_suffix)
                else string(R.string.vm_queued_suffix)
                _notice.value = Notice(string(R.string.vm_sent, suffix))
                loadDetail(sessionId)
                onSent?.invoke()
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) handleApiError(err) else _notice.value = Notice(err.message, isError = true)
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_send_failed), isError = true)
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
                api.approve(sessionId, requestId, scope, remoteNodeId())
                _notice.value = Notice(
                    if (scope == "session") string(R.string.vm_approved_session)
                    else string(R.string.vm_approved),
                )
                // Leaving the detail screen is the caller's job: the request may
                // already be gone from the next list read, and a detail screen
                // for a resolved request would keep offering dead buttons.
                refresh()
            } catch (err: WatchApiException) {
                // 409 means someone (or another client) already resolved it. That
                // is a normal race on a shared queue, so it gets a plain notice.
                val msg = if (err.statusCode == 409) string(R.string.vm_already_handled) else err.message
                _notice.value = Notice(msg, isError = err.statusCode != 409)
                refresh()
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_approve_failed), isError = true)
            }
        }
    }

    fun deny(item: WatchApproval) {
        val sessionId = item.sessionId ?: return
        val requestId = item.requestId ?: return
        viewModelScope.launch {
            try {
                api.deny(sessionId, requestId, nodeId = remoteNodeId())
                _notice.value = Notice(string(R.string.vm_denied))
                refresh()
            } catch (err: WatchApiException) {
                _notice.value = Notice(err.message, isError = true)
                refresh()
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_deny_failed), isError = true)
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

    /**
     * Loads the recent conversation for the current session.
     *
     * Re-reads even when the list is already populated rather than trusting the
     * cached copy: the user opening history usually does so right after a turn
     * finished, which is exactly when the cache is stale. An empty result is kept
     * as an empty list (not an error) so the screen can say "nothing yet" instead
     * of showing a failure for a brand-new session.
     */
    fun loadHistory(sessionId: String) {
        if (sessionId.isBlank()) return
        viewModelScope.launch {
            _historyLoading.value = true
            try {
                val loaded = api.sessionHistory(sessionId, nodeId = remoteNodeId())
                _history.value = loaded.messages
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) {
                    handleApiError(err)
                } else {
                    _history.value = emptyList()
                    _notice.value = Notice(err.message, isError = true)
                }
            } catch (err: Exception) {
                _history.value = emptyList()
                _notice.value = Notice(err.message ?: string(R.string.vm_history_failed), isError = true)
            } finally {
                _historyLoading.value = false
            }
        }
    }

    /**
     * Opens an approval by raw key, for notification deep links (mockups 06/08).
     *
     * The alert carries a key rather than an item, and it may be tapped after the
     * queue has already been refreshed (or before the first refresh completes), so
     * this cannot look the request up in local state. Setting the key alone is
     * enough: [openApprovalKey] is re-resolved against the latest queue on every
     * recomposition, so the screen shows the request as soon as it is loaded and
     * falls back to its "no longer pending" state when it is gone.
     */
    fun openApprovalByKey(key: String) {
        if (key.isBlank()) return
        _openApprovalKey.value = key
    }

    fun closeApproval() {
        _openApprovalKey.value = null
    }

    /**
     * Speaks the current session's latest reply (B7).
     *
     * The node derives the text (its own wrist-shortened summary), so the watch
     * does not re-implement that shaping and speaks exactly what it displays.
     *
     * A session with nothing to read back answers 404, which is reported as a plain
     * notice rather than an error: "nothing to hear yet" is information, not a
     * failure.
     */
    fun speakLatestReply() {
        val sessionId = _state.value.currentSessionId
        if (sessionId.isEmpty()) {
            _notice.value = Notice(string(R.string.vm_select_session_first), isError = true)
            return
        }
        if (_speaking.value) {
            stopSpeaking()
            return
        }
        viewModelScope.launch {
            _speaking.value = true
            try {
                val wav = api.synthesizeSession(sessionId)
                val played = playback.play(wav)
                if (!played) {
                    _notice.value = Notice(string(R.string.vm_speak_unsupported), isError = true)
                }
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) {
                    handleApiError(err)
                    return@launch
                }
                val msg = if (err.statusCode == 404) string(R.string.vm_no_reply_to_speak) else err.message
                _notice.value = Notice(msg, isError = err.statusCode != 404)
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_speak_failed), isError = true)
            } finally {
                _speaking.value = false
            }
        }
    }

    /** Stops playback early (the user tapped again, or left the screen). */
    fun stopSpeaking() {
        playback.stop()
        _speaking.value = false
    }
    /**
     * Begins push-to-talk capture.
     *
     * Returns false when the microphone is unavailable so the UI can ask for the
     * permission instead of pretending to listen.
     */
    fun startRecording(): Boolean {
        if (!voice.hasMicrophonePermission()) {
            _notice.value = Notice(string(R.string.vm_mic_permission), isError = true)
            return false
        }
        val started = voice.start()
        if (!started) {
            _notice.value = Notice(string(R.string.vm_record_start_failed), isError = true)
            return false
        }
        _recording.value = true
        return true
    }

    /**
     * Stops capture, uploads the clip, and sends the transcript as a command.
     *
     * [onSent] is threaded through to [sendCommand] rather than invoked here, so
     * it fires only when the node actually accepted the instruction. A capture
     * that produced no audio, failed to transcribe, or was rejected must leave the
     * compose screen in place so the user can try again.
     */
    fun stopRecordingAndSend(onSent: (() -> Unit)? = null) {
        if (!_recording.value) return
        _recording.value = false
        viewModelScope.launch {
            val file = try {
                voice.stop()
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_record_failed), isError = true)
                return@launch
            }
            if (file == null) {
                _notice.value = Notice(string(R.string.vm_no_audio), isError = true)
                return@launch
            }
            try {
                val result = api.transcribe(file)
                if (result.text.isBlank()) {
                    _notice.value = Notice(string(R.string.vm_no_transcript), isError = true)
                } else {
                    // Provenance stays "voice" end-to-end so the transcript can
                    // be analysed separately from typed input later.
                    sendCommand(result.text, source = "voice", onSent = onSent)
                }
            } catch (err: WatchApiException) {
                if (err.isAuthFailure) {
                    handleApiError(err)
                } else {
                    _notice.value = Notice(err.message, isError = true)
                }
            } catch (err: Exception) {
                _notice.value = Notice(err.message ?: string(R.string.vm_transcribe_failed), isError = true)
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
            // Stop the background pull: there is no credential left to read with,
            // and leaving it scheduled would keep waking the radio on a watch that
            // has been deliberately unpaired.
            WatchSync.cancel(getApplication())
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
                _notice.value = Notice(string(R.string.vm_credential_revoked), isError = true)
            }
            return
        }
        _state.value = _state.value.copy(loading = false, error = err.message)
    }
}
