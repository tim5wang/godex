package com.godex.wear.data

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * Wire DTOs for the godex watch surface (`/watch/...`).
 *
 * Field names are snake_case on the wire and are mapped explicitly rather than
 * relying on a global naming strategy, so a server-side rename shows up here as
 * a compile-time-ish failure in one file instead of silent nulls everywhere.
 */

@Serializable
data class PairingStart(
    @SerialName("device_code") val deviceCode: String,
    @SerialName("user_code") val userCode: String,
    @SerialName("expires_at") val expiresAt: String? = null,
    val interval: Int = 5,
    val approved: Boolean = false,
)

@Serializable
data class WatchDevice(
    @SerialName("device_id") val deviceId: String,
    val name: String = "",
    val kind: String = "watch",
    val scopes: List<String> = emptyList(),
    @SerialName("created_at") val createdAt: String? = null,
    @SerialName("last_seen") val lastSeen: String? = null,
    @SerialName("expires_at") val expiresAt: String? = null,
    val revoked: Boolean = false,
) {
    fun canRead(): Boolean = scopes.contains(SCOPE_READ)
    fun canCommand(): Boolean = scopes.contains(SCOPE_COMMAND)
    fun canApprove(): Boolean = scopes.contains(SCOPE_APPROVE)

    companion object {
        const val SCOPE_READ = "watch:read"
        const val SCOPE_COMMAND = "watch:command"
        const val SCOPE_APPROVE = "watch:approve"
    }
}

@Serializable
data class PairingToken(
    val status: String,
    val device: WatchDevice? = null,
    val token: String? = null,
    @SerialName("expires_at") val expiresAt: String? = null,
) {
    val isPending: Boolean get() = status == "pending"
    val isApproved: Boolean get() = status == "approved"
    val isExpired: Boolean get() = status == "expired"
}

@Serializable
data class WatchSession(
    @SerialName("session_id") val sessionId: String,
    val title: String = "",
    val running: Boolean = false,
    @SerialName("parent_session_id") val parentSessionId: String? = null,
    @SerialName("last_activity_at") val lastActivityAt: String? = null,
    // Turn-level identity. running→not-running is too coarse to alert on: a turn
    // that starts and ends between two polls leaves the flag exactly as it was, so
    // the only turns ever announced were the long ones. Comparing the last turn id
    // and its terminal status instead makes short turns visible. Both are absent
    // from older nodes, which degrades to the old running-based behaviour.
    @SerialName("last_turn_id") val lastTurnId: String? = null,
    @SerialName("last_turn_status") val lastTurnStatus: String? = null,
    @SerialName("last_turn_completed_at") val lastTurnCompletedAt: String? = null,
) {
    /** True when the newest turn has reached a state the user would want to know about. */
    val lastTurnFinished: Boolean
        get() = lastTurnStatus == "completed" || lastTurnStatus == "failed"
}

@Serializable
data class WatchNode(
    val id: String,
    val name: String = "",
    val status: String = "",
    @SerialName("last_seen") val lastSeen: String? = null,
    // What the target is (pc | cloud | phone), so the remote-control screen can
    // label it instead of showing every node as an interchangeable box. Empty
    // means a node that never declared a kind, which the node side defaults to pc.
    val kind: String = "",
) {
    val isOnline: Boolean get() = status.equals("online", ignoreCase = true)

    // The display label for `kind` lives in ui/Theme.kt as nodeKindLabel(): it is a
    // translation, and a DTO cannot reach string resources.
}

@Serializable
data class WatchApproval(
    val kind: String,
    @SerialName("session_id") val sessionId: String? = null,
    @SerialName("session_title") val sessionTitle: String? = null,
    @SerialName("node_name") val nodeName: String? = null,
    @SerialName("request_id") val requestId: String? = null,
    @SerialName("tool_name") val toolName: String? = null,
    val summary: String = "",
    @SerialName("created_at") val createdAt: String? = null,
    // Approval-detail fields (mockup 06): the single-approval screen must show
    // what will actually run, so the list's 140-rune summary is not enough.
    val command: String? = null,
    val risk: String? = null,
    @SerialName("risk_level") val riskLevel: String? = null,
    @SerialName("expires_at") val expiresAt: String? = null,
    @SerialName("task_id") val taskId: String? = null,
    @SerialName("run_id") val runId: String? = null,
    val queue: String? = null,
    val prompt: String? = null,
) {
    val isPermission: Boolean get() = kind == "permission"
    val isFlowTask: Boolean get() = kind == "flow_task"

    /** True when the risk verdict says an irreversible command is about to run. */
    val isHighRisk: Boolean get() = riskLevel == "high"

    /** The command to echo verbatim, falling back to the truncated summary. */
    val commandText: String get() = command?.takeIf { it.isNotBlank() } ?: summary

    /**
     * Stable identity for this request, used to re-find it after a refresh.
     *
     * The detail screen holds a key rather than the item itself: the approval
     * list is re-read by the refresh loop, so a captured item would show stale
     * contents and could keep offering buttons for a request another client
     * already resolved.
     */
    val key: String get() = if (isPermission) {
        "perm:${sessionId.orEmpty()}:${requestId.orEmpty()}"
    } else {
        "task:${taskId.orEmpty()}"
    }
}

/**
 * Aggregated home-screen payload.
 *
 * The watch is a battery-constrained client, so the home screen prefers one
 * round-trip over four independent calls.
 */
@Serializable
data class WatchOverview(
    val devices: List<WatchNode> = emptyList(),
    @SerialName("current_session") val currentSession: WatchSession? = null,
    @SerialName("active_sessions") val activeSessions: Int = 0,
    @SerialName("pending_approvals") val pendingApprovals: Int = 0,
    @SerialName("server_time") val serverTime: String? = null,
)

@Serializable
data class WatchSessionDetail(
    @SerialName("session_id") val sessionId: String,
    val title: String = "",
    val running: Boolean = false,
    @SerialName("active_phase") val activePhase: String? = null,
    @SerialName("active_turn_id") val activeTurnId: String? = null,
    @SerialName("pending_approvals") val pendingApprovals: Int = 0,
    @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("started_at") val startedAt: String? = null,
    @SerialName("last_turn") val lastTurn: LastTurn? = null,
    @SerialName("last_message") val lastMessage: LastMessage? = null,
    // B4: the node's wrist-shortened form of the agent's last reply. Preferred over
    // lastMessage.text, which is only a raw 140-rune cut and so often showed a
    // markdown heading or the opening of a code fence instead of a sentence.
    @SerialName("reply_summary") val replySummary: String? = null,
    @SerialName("current_tool") val currentTool: String? = null,
    // The recent tool sequence (mockup 05's "what has it been doing"), bounded
    // server-side. Distinct from currentTool, which is only the most recent name.
    @SerialName("recent_tools") val recentTools: List<ToolEvent> = emptyList(),
    val steps: List<Step> = emptyList(),
    @SerialName("step_done") val stepDone: Int = 0,
    @SerialName("step_total") val stepTotal: Int = 0,
    @SerialName("progress_percent") val progressPercent: Int = 0,
) {
    @Serializable
    data class LastTurn(
        @SerialName("turn_id") val turnId: String = "",
        val status: String = "",
        @SerialName("updated_at") val updatedAt: String? = null,
    )

    /**
     * One tool call the agent made, as reported by the node.
     *
     * `pending` comes from the server rather than being inferred from a zero
     * duration: a call that finishes in under a millisecond also reports 0ms, and
     * showing that as still running would misstate the agent's state.
     */
    @Serializable
    data class ToolEvent(
        val name: String = "",
        val failed: Boolean = false,
        @SerialName("timed_out") val timedOut: Boolean = false,
        @SerialName("duration_ms") val durationMs: Long = 0,
        val pending: Boolean = false,
    ) {
        /** "1.2s" / "340ms" — a wrist needs scale, not precision. */
        val durationText: String
            get() = when {
                durationMs <= 0 -> ""
                durationMs < 1000 -> "${durationMs}ms"
                else -> "%.1fs".format(durationMs / 1000.0)
            }
    }

    @Serializable
    data class LastMessage(
        val role: String = "",
        val text: String = "",
    )

    /**
     * One entry of the agent's own plan.
     *
     * The server derives this from the session todo list; the watch does not
     * invent progress, it only renders what the agent actually recorded.
     */
    @Serializable
    data class Step(
        val content: String = "",
        val status: String = "",
    ) {
        val isDone: Boolean get() = status == "completed"
        val isActive: Boolean get() = status == "in_progress"
    }

    /** True when the server reported a live step list worth rendering. */
    val hasSteps: Boolean get() = steps.isNotEmpty()

    /**
     * True when there is a turn to stop. Drives whether "stop" is offered at
     * all — a permanently visible dead button is worse than no button.
     */
    val hasRunningTurn: Boolean get() = !activeTurnId.isNullOrEmpty()
}

/**
 * One session tagged with the node it lives on (mockup 14).
 *
 * Flattened rather than nested so the same list rendering path handles both the
 * local session list and the cross-device one.
 */
@Serializable
data class WatchCrossDeviceSession(
    @SerialName("session_id") val sessionId: String,
    val title: String = "",
    val running: Boolean = false,
    @SerialName("parent_session_id") val parentSessionId: String? = null,
    @SerialName("last_activity_at") val lastActivityAt: String? = null,
    @SerialName("node_id") val nodeId: String = "",
    @SerialName("node_name") val nodeName: String = "",
    val online: Boolean = true,
    val remote: Boolean = false,
)

@Serializable
data class CommandRequest(
    @SerialName("session_id") val sessionId: String,
    val text: String,
    val source: String = "text",
    val metadata: Map<String, String> = emptyMap(),
)

@Serializable
data class CommandAccepted(
    @SerialName("session_id") val sessionId: String = "",
    @SerialName("turn_id") val turnId: String = "",
    val status: String = "",
    @SerialName("pending_approval") val pendingApproval: Boolean = false,
)

@Serializable
data class ApprovalResponse(
    val scope: String? = null,
)

@Serializable
data class DenyResponse(
    val reason: String? = null,
)

@Serializable
data class TranscribeResult(
    val text: String = "",
    val confidence: Double = 0.0,
)

/**
 * A preset the wrist may start a conversation with.
 *
 * Deliberately narrower than the node's full AgentTemplate: bundles, tools and
 * MCP servers are not things anyone can meaningfully choose between on a watch,
 * and shipping them would bloat a list meant to fit on one screen.
 */
@Serializable
data class WatchTemplate(
    val id: String,
    val name: String = "",
    val description: String = "",
    val avatar: String = "",
)

@Serializable
data class CreateSessionRequest(
    @SerialName("template_id") val templateId: String = "",
    val title: String = "",
)

/** Server error envelope; godex returns `{"error": "..."}` on failures. */
@Serializable
data class ErrorBody(val error: String = "")
