package com.godex.wear.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.Chip
import androidx.wear.compose.material.ChipDefaults
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R

/**
 * Home: the one screen that answers "is anything waiting for me?".
 *
 * Layout mirrors mockup 03 — a status line, the current target, and two large
 * actions. Everything else is one tap away, because a wrist glance lasts a
 * couple of seconds.
 */
@Composable
fun HomeScreen(
    state: WatchViewModel.UiState,
    onTalk: () -> Unit,
    onSessions: () -> Unit,
    onApprovals: () -> Unit,
    onDevices: () -> Unit,
    onRefresh: () -> Unit,
    onDetail: () -> Unit,
    onSettings: () -> Unit,
) {
    val detail = state.detail
    val session = state.sessions.firstOrNull { it.sessionId == state.currentSessionId }
    val pending = state.approvals.size
    val running = detail?.running ?: session?.running ?: false

    RoundScreen {
        Column(
            modifier = Modifier.fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            ScreenTitle(stringResource(R.string.app_name))

            Spacer(Modifier.height(6.dp))
            when {
                state.error != null -> MessageBlock("连接失败", state.error, GodexColors.Danger)
                state.loading && state.sessions.isEmpty() -> MessageBlock("加载中…")
                session == null -> MessageBlock("没有会话", "在电脑端开始一个")
                else -> {
                    // Tapping the session name opens the detail screen (mockup 05):
                    // the home screen answers "anything waiting?", the detail screen
                    // answers "what is it doing?".
                    //
                    // The title itself is the tap target rather than a separate row:
                    // this Column does not scroll, so an extra control pushed 设备 /
                    // 刷新 off the bottom of the round display entirely (caught by
                    // driving the emulator, not by the compiler).
                    Text(
                        text = session.title.ifBlank { "未命名会话" },
                        style = MaterialTheme.typography.body1,
                        color = GodexColors.TextPrimary,
                        textAlign = TextAlign.Center,
                        maxLines = 2,
                        overflow = TextOverflow.Ellipsis,
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable(onClick = onDetail),
                    )
                    Spacer(Modifier.height(4.dp))
                    // The hint lives in the status line so the affordance is
                    // visible without spending any vertical space. A pending
                    // approval joins this same line: this Column does not scroll,
                    // and giving the count its own row pushed the
                    // 设备 / 刷新 / 设置 row below the round display's bottom edge
                    // (caught on the emulator — those nodes were absent from the
                    // view hierarchy entirely, and swiping could not reveal them).
                    // The consequence was the worst possible: precisely when an
                    // approval was waiting, the device switcher and refresh became
                    // unreachable. One line states both facts at no extra height.
                    StatusDot(
                        color = when {
                            pending > 0 -> GodexColors.Attention
                            running -> GodexColors.Accent
                            else -> GodexColors.TextMuted
                        },
                        label = buildString {
                            append(if (running) "运行中" else "空闲")
                            if (pending > 0) append(" · $pending 项待批准")
                            append(" · 点按看详情")
                        },
                    )
                }
            }

            Spacer(Modifier.height(10.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceEvenly,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                RoundAction(
                    label = if (running) "说话" else "说话",
                    onClick = onTalk,
                    modifier = Modifier.weight(1f),
                    enabled = state.currentSessionId.isNotEmpty(),
                )
            }
            Spacer(Modifier.height(6.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(ChipSpacing),
            ) {
                SmallAction("会话", onSessions, Modifier.weight(1f))
                SmallAction(
                    label = if (pending > 0) "审批 $pending" else "审批",
                    onClick = onApprovals,
                    modifier = Modifier.weight(1f),
                    highlight = pending > 0,
                )
            }
            Spacer(Modifier.height(6.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.spacedBy(ChipSpacing),
            ) {
                SmallAction("设备", onDevices, Modifier.weight(1f))
                SmallAction("刷新", onRefresh, Modifier.weight(1f))
                // Settings lives in this existing row rather than a new one: this
                // Column does not scroll, and an added row pushed 设备/刷新 off the
                // round display (seen on the emulator). A third chip changes no
                // height. Before this, 设置 was only reachable from the pairing
                // screen, so a paired watch could never open mockup 11 at all.
                SmallAction("设置", onSettings, Modifier.weight(1f))
            }
        }
    }
}

/** Compact secondary action; used where a full-width chip would overflow. */
@Composable
fun SmallAction(
    label: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    highlight: Boolean = false,
    enabled: Boolean = true,
) {
    androidx.wear.compose.material.Chip(
        label = {
            Text(
                text = label,
                style = MaterialTheme.typography.caption1,
                textAlign = TextAlign.Center,
                maxLines = 1,
                modifier = Modifier.fillMaxWidth().padding(horizontal = 2.dp),
            )
        },
        onClick = onClick,
        // Disabled is rendered by the chip itself so the label stays legible: a
        // hidden control would leave no clue that the action exists at all.
        enabled = enabled,
        colors = ChipDefaults.secondaryChipColors(
            backgroundColor = if (highlight) GodexColors.Attention else GodexColors.SurfaceHigh,
            contentColor = if (highlight) androidx.compose.ui.graphics.Color.Black else GodexColors.TextSecondary,
        ),
        modifier = modifier,
    )
}
