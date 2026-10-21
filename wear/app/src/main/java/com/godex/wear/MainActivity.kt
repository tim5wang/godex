package com.godex.wear

import android.Manifest
import android.content.Intent
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.LifecycleStartEffect
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.SwipeToDismissBox
import androidx.wear.compose.material.Text
import com.godex.wear.ui.ApprovalDetailScreen
import com.godex.wear.ui.ApprovalsScreen
import com.godex.wear.ui.CrossDeviceScreen
import com.godex.wear.ui.DevicesScreen
import com.godex.wear.ui.GodexWearTheme
import com.godex.wear.ui.HomeScreen
import com.godex.wear.ui.PairingScreen
import com.godex.wear.ui.RoundScreen
import com.godex.wear.ui.ScreenTitle
import com.godex.wear.ui.SessionDetailScreen
import com.godex.wear.ui.SessionsScreen
import com.godex.wear.ui.SettingsScreen
import com.godex.wear.ui.TalkScreen
import com.godex.wear.ui.WatchViewModel

/**
 * Single activity hosting every screen.
 *
 * Navigation is a plain sealed state rather than a nav library: the graph is
 * seven flat destinations with no deep links, so a state enum is fewer moving
 * parts (and fewer dependencies) than a router — which matters on a watch.
 */
class MainActivity : ComponentActivity() {

    /**
     * Session id carried by a notification deep link (mockup 08), if any.
     *
     * Held in state rather than read once in `onCreate` because the activity is
     * `singleTop`: tapping a second notification while the app is already open
     * delivers [onNewIntent] instead of restarting the activity.
     */
    private var deepLinkSessionId by mutableStateOf("")

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        deepLinkSessionId = intent?.getStringExtra(com.godex.wear.notify.WearNotifier.EXTRA_SESSION_ID).orEmpty()
        setContent { GodexWearTheme { GodexWearRoot(deepLinkSessionId) } }
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        setIntent(intent)
        deepLinkSessionId = intent.getStringExtra(com.godex.wear.notify.WearNotifier.EXTRA_SESSION_ID).orEmpty()
    }
}

private sealed interface Screen {
    data object Home : Screen
    data object Talk : Screen
    data object Sessions : Screen
    data object Approvals : Screen
    data object Devices : Screen
    data object Settings : Screen
    data object Pairing : Screen

    /** Task detail for the session the remote control is pointed at (mockup 05). */
    data object Detail : Screen

    /** Unified list across every node this center can see (mockup 14). */
    data object CrossDevice : Screen

    /**
     * Single-approval decision screen (mockup 06).
     *
     * Separate from the queue on purpose: the queue is for triage, this is where
     * an irreversible command is actually authorised, so it shows the full
     * command, the node and the risk verdict before any button is pressed.
     */
    data object ApprovalDetail : Screen
}

@Composable
private fun GodexWearRoot(deepLinkSessionId: String = "") {
    val vm: WatchViewModel = viewModel()
    val state by vm.state.collectAsState()
    val recording by vm.recording.collectAsState()
    val notice by vm.notice.collectAsState()
    // Hoisted rather than collected inside the navigation branch: collecting state
    // conditionally is fragile (the subscription depends on which screen is
    // showing) and the approval detail screen re-resolves against it.
    val openApprovalKey by vm.openApprovalKey.collectAsState()

    var screen by remember { mutableStateOf<Screen>(Screen.Home) }

    val micPermission = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { granted ->
        if (granted) {
            // Retry immediately so the user's original press is not wasted.
            vm.startRecording()
        }
    }

    val notificationPermission = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission(),
    ) { /* Denial is tolerated: the in-app approval queue still works. */ }

    val credentials = state.credentials
    val paired = credentials?.isUsable == true

    // Unpaired watches can only pair; there is nothing to remote-control yet.
    LaunchedEffect(paired) {
        if (!paired && screen == Screen.Home) screen = Screen.Pairing
        if (paired && screen == Screen.Pairing) screen = Screen.Home
    }

    // Ask for notification permission once paired, when there is finally
    // something worth notifying about. Asking on first launch, before the watch
    // has any reason to notify, is the reliable way to get a reflex denial.
    LaunchedEffect(paired) {
        if (paired && Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }
    }

    /**
     * Runs the refresh loop only while the UI is visible.
     *
     * Tied to the lifecycle instead of the ViewModel's lifetime because a watch
     * is mostly off-wrist, face-down or in ambient mode: polling then would be
     * pure battery drain. [LifecycleStartEffect] re-arms on every return to the
     * foreground, so the user never sees a stale screen after raising their
     * wrist.
     */
    LifecycleStartEffect(paired) {
        if (paired) vm.startPolling()
        onStopOrDispose { vm.stopPolling() }
    }

    // Notification deep link (mockup 08): open the session the alert referred to
    // rather than whichever session happens to be selected.
    LaunchedEffect(deepLinkSessionId, paired) {
        if (!paired || deepLinkSessionId.isEmpty()) return@LaunchedEffect
        vm.selectSession(deepLinkSessionId)
        screen = Screen.Detail
    }

    Box(modifier = Modifier.fillMaxSize()) {
        when {
            !paired -> PairingScreen(
                api = vm.api,
                serverUrl = credentials?.baseUrl?.takeIf { it.isNotEmpty() }
                    ?: com.godex.wear.data.WatchApi.DEFAULT_EMULATOR_URL,
                onServerUrlChange = vm::setServerUrl,
                onPaired = vm::onPaired,
                onSettings = { screen = Screen.Settings },
            )

            else -> when (val current = screen) {
                Screen.Home -> HomeScreen(
                    state = state,
                    onTalk = { screen = Screen.Talk },
                    onSessions = { screen = Screen.Sessions },
                    onApprovals = { screen = Screen.Approvals },
                    onDevices = { screen = Screen.Devices },
                    onRefresh = vm::refresh,
                    onDetail = { screen = Screen.Detail },
                    onSettings = { screen = Screen.Settings },
                )

                Screen.Talk -> TalkScreen(
                    sessionTitle = state.sessions
                        .firstOrNull { it.sessionId == state.currentSessionId }
                        ?.title.orEmpty(),
                    recording = recording,
                    onStartRecording = {
                        // Requesting the permission mid-gesture is safe: the
                        // recorder only starts once it is granted.
                        if (vm.voice.hasMicrophonePermission()) vm.startRecording()
                        else {
                            micPermission.launch(Manifest.permission.RECORD_AUDIO)
                            false
                        }
                    },
                    onStopRecording = vm::stopRecordingAndSend,
                    onCancelRecording = vm::cancelRecording,
                    onSendText = { vm.sendCommand(it) },
                    onBack = { screen = Screen.Home },
                )

                Screen.Sessions -> SessionsScreen(
                    sessions = state.sessions,
                    currentSessionId = state.currentSessionId,
                    pinned = state.pinned,
                    loading = state.loading,
                    onSelect = {
                        vm.selectSession(it)
                        screen = Screen.Home
                    },
                    onTogglePin = vm::togglePin,
                    onCrossDevice = { screen = Screen.CrossDevice },
                    onBack = { screen = Screen.Home },
                )

                Screen.Detail -> SessionDetailScreen(
                    detail = state.detail,
                    sessionId = state.currentSessionId,
                    loading = state.loading,
                    onStop = vm::stopCurrentSession,
                    onBack = { screen = Screen.Home },
                    onTalk = { screen = Screen.Talk },
                )

                Screen.CrossDevice -> CrossDeviceScreen(
                    sessions = state.crossDevice,
                    currentSessionId = state.currentSessionId,
                    loading = state.loading,
                    onSelect = {
                        vm.selectSession(it)
                        screen = Screen.Home
                    },
                    onBack = { screen = Screen.Sessions },
                )

                Screen.Approvals -> ApprovalsScreen(
                    approvals = state.approvals,
                    loading = state.loading,
                    onOpen = {
                        vm.openApproval(it)
                        screen = Screen.ApprovalDetail
                    },
                    onApprove = { vm.approve(it) },
                    onDeny = vm::deny,
                    onBack = { screen = Screen.Home },
                )

                Screen.ApprovalDetail -> {
                    // Re-resolved from the latest queue on every recomposition
                    // rather than held as a captured object, so a request that
                    // another client resolved disappears instead of lingering
                    // with dead buttons.
                    val approvals = state.approvals
                    val index = approvals.indexOfFirst { it.key == openApprovalKey }
                    ApprovalDetailScreen(
                        approval = approvals.getOrNull(index),
                        index = if (index >= 0) index + 1 else 0,
                        total = approvals.size,
                        onApprove = { item, scope ->
                            vm.approve(item, scope)
                            vm.closeApproval()
                            screen = Screen.Approvals
                        },
                        onDeny = {
                            vm.deny(it)
                            vm.closeApproval()
                            screen = Screen.Approvals
                        },
                        onBack = {
                            vm.closeApproval()
                            screen = Screen.Approvals
                        },
                    )
                }

                Screen.Devices -> DevicesScreen(
                    nodes = state.nodes,
                    targetDeviceId = state.targetDeviceId,
                    loading = state.loading,
                    onSelect = {
                        vm.setTargetDevice(it)
                        screen = Screen.Home
                    },
                    onBack = { screen = Screen.Home },
                )

                Screen.Settings -> SettingsScreen(
                    credentials = credentials,
                    preferences = state.preferences,
                    onSaveUrl = {
                        vm.setServerUrl(it)
                        screen = Screen.Home
                    },
                    onNotifyOnComplete = vm::setNotifyOnComplete,
                    onPowerSaving = vm::setPowerSaving,
                    onAmbient = vm::setAmbient,
                    onUnpair = {
                        vm.unpair()
                        screen = Screen.Pairing
                    },
                    onBack = { screen = if (paired) Screen.Home else Screen.Pairing },
                )

                Screen.Pairing -> PairingScreen(
                    api = vm.api,
                    serverUrl = credentials?.baseUrl?.takeIf { it.isNotEmpty() }
                        ?: com.godex.wear.data.WatchApi.DEFAULT_EMULATOR_URL,
                    onServerUrlChange = vm::setServerUrl,
                    onPaired = vm::onPaired,
                    onSettings = { screen = Screen.Settings },
                )
            }
        }

        // Transient feedback (sent / approved / failed). Rendered as an overlay so
        // it never displaces the screen content underneath.
        notice?.let { current ->
            NoticeOverlay(text = current.text, isError = current.isError, onDismiss = vm::dismissNotice)
        }
    }
}

/**
 * Brief message that dismisses itself.
 *
 * Wear has no snackbar host, and a swipe-to-dismiss box keeps the user in
 * control when they want it gone immediately.
 */
@Composable
private fun NoticeOverlay(text: String, isError: Boolean, onDismiss: () -> Unit) {
    LaunchedEffect(text) {
        kotlinx.coroutines.delay(2600)
        onDismiss()
    }

    SwipeToDismissBox(
        onDismissed = { onDismiss() },
        modifier = Modifier.fillMaxSize(),
    ) {
        RoundScreen {
            ScreenTitle(
                text = text,
                subtitle = if (isError) "点按或滑动关闭" else null,
            )
        }
    }
}

/** Kept for the (currently unused) non-Compose path. */
@Composable
private fun FallbackLabel() {
    MaterialTheme {
        Box(modifier = Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            Text(text = stringResource(R.string.app_name))
        }
    }
}
