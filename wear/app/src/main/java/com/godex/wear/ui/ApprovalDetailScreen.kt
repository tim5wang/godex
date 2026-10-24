package com.godex.wear.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.foundation.lazy.ScalingLazyColumn
import androidx.wear.compose.foundation.lazy.rememberScalingLazyListState
import androidx.wear.compose.material.Chip
import androidx.wear.compose.material.ChipDefaults
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.data.WatchApproval

/**
 * Single-approval screen (mockup 06) — the most consequential screen in the app.
 *
 * It shows what will actually run, and on which node, before the user authorises
 * it. The queue (mockup 15) is for triage; this screen is the decision.
 *
 * **Deviation from mockup 06 (long-press).** The mockup grants "本会话全部允许" by
 * long-pressing 批准. Wear Compose 1.4's `Chip` exposes no `onLongClick` (verified
 * against the compiled artifact; the same limitation is already documented on the
 * session switcher), so broadening the grant is an explicit chip. A hidden
 * long-press is also undiscoverable, and this is precisely the screen where the
 * user must understand how far an approval reaches.
 *
 * **Safety.** Mockups 11/15 require a high-risk approval to be confirmed rather
 * than tapped. With no long-press available the equivalent is a two-step tap: the
 * first arms, the second commits. Arming is keyed by request, so arming one
 * request can never commit another, and it also gates the broader grant — if
 * approving once needs confirmation, approving for a whole session cannot be the
 * easier action.
 *
 * [loading] distinguishes "not fetched yet" from "no longer pending". A
 * notification tap opens this screen directly, often during a cold start, when
 * the queue is legitimately still empty — without this the screen would announce
 * 已处理 for a request the user has not answered.
 */
@Composable
fun ApprovalDetailScreen(
    approval: WatchApproval?,
    index: Int,
    total: Int,
    loading: Boolean = false,
    onApprove: (WatchApproval, String) -> Unit,
    onDeny: (WatchApproval) -> Unit,
    onBack: () -> Unit,
) {
    val listState = rememberScalingLazyListState()

    // Armed state is keyed by request so it cannot leak across requests.
    var armedKey by remember(approval?.key) { mutableStateOf<String?>(null) }
    val armed = approval != null && armedKey == approval.key

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier.fillMaxSize(),
    ) {
        if (approval == null) {
            // Two different reasons for an absent request, and conflating them
            // would mislead: still loading (cold start after a notification tap)
            // versus actually gone (resolved from another client). Saying
            // "handled" while the first fetch is in flight would be a lie, and on
            // an approval screen a lie about state is the one thing that must not
            // happen.
            item {
                if (loading) {
                    ScreenTitle("加载中…", "正在读取待批准项")
                } else {
                    ScreenTitle("已处理", "该请求已不在队列中")
                }
            }
            item {
                SmallAction(label = "返回", onClick = onBack, modifier = Modifier.fillMaxWidth())
            }
            return@ScalingLazyColumn
        }

        item {
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.Center,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                StatusDot(
                    color = if (approval.isHighRisk) GodexColors.Danger else GodexColors.Attention,
                    label = if (total > 1) "待审批 $index/$total" else "待审批 $index/$index",
                )
                relativeTime(approval.createdAt).takeIf { it.isNotEmpty() }?.let {
                    Text(
                        text = "  $it",
                        style = MaterialTheme.typography.caption1,
                        color = GodexColors.TextMuted,
                    )
                }
            }
        }

        item {
            Spacer(Modifier.height(6.dp))
            Column(
                modifier = Modifier.fillMaxWidth(),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(
                    text = if (approval.isHighRisk) "!" else "·",
                    style = MaterialTheme.typography.title2,
                    color = if (approval.isHighRisk) GodexColors.Danger else GodexColors.Attention,
                    textAlign = TextAlign.Center,
                )
                Text(
                    text = if (approval.isHighRisk) "agent 请求执行高危命令" else "agent 请求批准工具调用",
                    style = MaterialTheme.typography.body1,
                    color = GodexColors.TextPrimary,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        // The panel answers "what exactly, and where" in one block: the tool, the
        // node it will run on, the session it belongs to, then the command itself.
        item {
            Spacer(Modifier.height(8.dp))
            Column(
                modifier = Modifier
                    .fillMaxWidth()
                    .background(GodexColors.Surface)
                    .padding(10.dp),
            ) {
                Text(
                    text = buildString {
                        append(approval.toolName?.uppercase().orEmpty().ifBlank { "TOOL" })
                        approval.nodeName?.takeIf { it.isNotBlank() }?.let { append(" · ").append(it) }
                        approval.sessionTitle?.takeIf { it.isNotBlank() }
                            ?.let { append(" · 会话「").append(it).append("」") }
                    },
                    style = MaterialTheme.typography.caption1,
                    color = if (approval.isHighRisk) GodexColors.Danger else GodexColors.TextSecondary,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(6.dp))
                // Full command, not the queue's truncated summary: authorising a
                // command you cannot read is not authorisation.
                Text(
                    text = approval.commandText,
                    style = MaterialTheme.typography.body1,
                    color = GodexColors.TextPrimary,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        // The risk verdict comes from the server's shared classifier rather than
        // being re-derived here, so the watch cannot disagree with the other
        // clients about how dangerous a command is.
        approval.risk?.takeIf { it.isNotBlank() }?.let { risk ->
            item {
                Spacer(Modifier.height(6.dp))
                Text(
                    text = "风险：$risk",
                    style = MaterialTheme.typography.caption1,
                    color = if (approval.isHighRisk) GodexColors.Danger else GodexColors.TextSecondary,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        if (approval.isPermission) item {
            Spacer(Modifier.height(4.dp))
            Text(
                text = buildString {
                    append("批准后")
                    append(if (approval.toolName.isNullOrBlank()) "该操作" else "该命令")
                    val node = approval.nodeName?.takeIf { it.isNotBlank() }
                    if (node != null) append("将立即在 ").append(node).append(" 执行") else append("将立即执行")
                    append("。")
                },
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }

        // Time left before the request expires. Purely informational: the server
        // prunes expired requests before listing them (PermissionManager.ListPending),
        // so one that is gone never reaches this screen at all. It is shown because
        // how long is left changes whether answering from the wrist is worth it — a
        // request with seconds remaining is better handled natively than answered
        // in a hurry. Rendered in the danger colour once it is nearly out of time.
        if (approval.isPermission) {
            val remaining = remainingTime(approval.expiresAt)
            if (remaining.isNotEmpty()) {
                item {
                    Spacer(Modifier.height(4.dp))
                    Text(
                        text = remaining,
                        style = MaterialTheme.typography.caption1,
                        color = GodexColors.TextMuted,
                        textAlign = TextAlign.Center,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            }
        }

        // Only a tool permission can be decided here. A flow human task needs a
        // form the watch cannot present, so it must not offer buttons that would
        // do nothing (approve/deny require a session id and request id, and a
        // flow task has neither).
        if (approval.isPermission) item {
            Spacer(Modifier.height(10.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(ChipSpacing),
            ) {
                SmallAction(
                    label = "拒绝",
                    onClick = { onDeny(approval) },
                    modifier = Modifier.weight(1f),
                )
                RoundAction(
                    label = if (approval.isHighRisk && armed) "确认批准" else "批准",
                    color = if (approval.isHighRisk) GodexColors.Danger else GodexColors.Accent,
                    onClick = {
                        // High-risk needs two taps; the first only arms.
                        if (approval.isHighRisk && !armed) armedKey = approval.key
                        else onApprove(approval, "once")
                    },
                    modifier = Modifier.weight(1f),
                )
            }
        }

        if (approval.isHighRisk) {
            item {
                Spacer(Modifier.height(4.dp))
                Text(
                    text = if (armed) "高危命令 · 再点一次即执行" else "高危命令 · 需点两次确认",
                    style = MaterialTheme.typography.caption1,
                    color = if (armed) GodexColors.Danger else GodexColors.TextMuted,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        // Broadening the grant is a separate, explicit decision — never the
        // default of the approve button.
        if (approval.isPermission) {
            item {
                Spacer(Modifier.height(6.dp))
                Chip(
                    label = {
                        Text(
                            text = "本会话全部允许",
                            style = MaterialTheme.typography.caption1,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.fillMaxWidth(),
                        )
                    },
                    onClick = { onApprove(approval, "session") },
                    // Gated by the same arming step: if one approval on a
                    // high-risk request needs confirming, granting it for the
                    // entire session must not be the easier path.
                    enabled = !approval.isHighRisk || armed,
                    colors = ChipDefaults.secondaryChipColors(
                        backgroundColor = GodexColors.SurfaceHigh,
                        contentColor = GodexColors.TextSecondary,
                    ),
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        if (approval.isFlowTask) {
            item {
                Spacer(Modifier.height(6.dp))
                Text(
                    text = "流程人工卡点需在电脑端填写表单",
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        item {
            Spacer(Modifier.height(6.dp))
            SmallAction(label = "返回", onClick = onBack, modifier = Modifier.fillMaxWidth())
        }
    }
}
