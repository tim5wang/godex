package com.godex.wear.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.wear.compose.foundation.lazy.ScalingLazyColumn
import androidx.wear.compose.foundation.lazy.items
import androidx.wear.compose.foundation.lazy.rememberScalingLazyListState
import androidx.wear.compose.material.Chip
import androidx.wear.compose.material.ChipDefaults
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R
import com.godex.wear.data.WatchApproval

/**
 * Approval queue (mockups 06 / 15).
 *
 * Tool permissions and flow human tasks share one list because from the wrist
 * they are the same decision. Approving is deliberately a separate tap from
 * selecting: an accidental approval grants a real capability on a real machine.
 */
@Composable
fun ApprovalsScreen(
    approvals: List<WatchApproval>,
    loading: Boolean,
    onOpen: (WatchApproval) -> Unit,
    onApprove: (WatchApproval) -> Unit,
    onDeny: (WatchApproval) -> Unit,
    onBack: () -> Unit,
) {
    val listState = rememberScalingLazyListState()

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier.fillMaxSize(),
    ) {
        item {
            ScreenTitle(
                stringResource(R.string.approvals_title),
                if (approvals.isEmpty()) stringResource(R.string.approvals_all_done)
                else stringResource(R.string.approvals_count, approvals.size),
            )
        }

        if (approvals.isEmpty()) {
            item {
                MessageBlock(
                    title = if (loading) stringResource(R.string.common_loading)
                    else stringResource(R.string.approvals_empty),
                    tint = if (loading) GodexColors.TextSecondary else GodexColors.Accent,
                )
            }
        }

        items(approvals, key = { keyOf(it) }) { item ->
            // Only tool-permission requests can be resolved from the watch: a flow
            // human task needs a form that a 454px screen cannot present, so it is
            // shown for awareness and resolved in a fuller client.
            val actionable = item.isPermission
            Column(modifier = Modifier.fillMaxWidth()) {
                Chip(
                    label = {
                        Text(
                            text = item.summary.ifBlank {
                                item.toolName ?: stringResource(R.string.approvals_title)
                            },
                            style = MaterialTheme.typography.body1,
                            maxLines = 2,
                            overflow = TextOverflow.Ellipsis,
                        )
                    },
                    secondaryLabel = {
                        Text(
                            text = buildString {
                                append(
                                    if (item.isPermission) stringResource(R.string.common_tool)
                                    else stringResource(R.string.common_flow_task),
                                )
                                item.toolName?.takeIf { it.isNotBlank() }?.let { append(" · ").append(it) }
                                relativeTime(item.createdAt).takeIf { it.isNotEmpty() }
                                    ?.let { append(" · ").append(it) }
                            },
                            style = MaterialTheme.typography.caption1,
                            maxLines = 1,
                        )
                    },
                    onClick = { if (actionable || item.isFlowTask) onOpen(item) },
                    colors = ChipDefaults.secondaryChipColors(
                        backgroundColor = if (actionable) GodexColors.Surface else GodexColors.SurfaceHigh,
                        contentColor = GodexColors.TextPrimary,
                    ),
                    modifier = Modifier.fillMaxWidth(),
                )
                if (actionable) {
                    Spacer(Modifier.height(4.dp))
                    androidx.compose.foundation.layout.Row(
                        modifier = Modifier.fillMaxWidth(),
                        horizontalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(ChipSpacing),
                    ) {
                        SmallAction(
                            label = stringResource(R.string.common_approve),
                            highlight = true,
                            onClick = { onApprove(item) },
                            modifier = Modifier.weight(1f),
                        )
                        SmallAction(
                            label = stringResource(R.string.common_deny),
                            onClick = { onDeny(item) },
                            modifier = Modifier.weight(1f),
                        )
                    }
                } else {
                    Spacer(Modifier.height(4.dp))
                    Text(
                        text = stringResource(R.string.approvals_form_hint),
                        style = MaterialTheme.typography.caption1,
                        color = GodexColors.TextMuted,
                    )
                }
                Spacer(Modifier.height(6.dp))
            }
        }

        item {
            SmallAction(
                label = stringResource(R.string.common_back),
                onClick = onBack,
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

/**
 * Stable list key. A permission is identified by session+request; a flow task by
 * its task id. Without this, Compose would key on list position and reuse the
 * wrong row after an approval is removed.
 */
private fun keyOf(item: WatchApproval): String = when {
    item.isPermission -> "perm:${item.sessionId}:${item.requestId}"
    else -> "task:${item.taskId}"
}
