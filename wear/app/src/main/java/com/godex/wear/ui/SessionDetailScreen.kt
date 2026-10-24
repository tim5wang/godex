package com.godex.wear.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.wear.compose.foundation.lazy.ScalingLazyColumn
import androidx.wear.compose.foundation.lazy.items
import androidx.wear.compose.foundation.lazy.rememberScalingLazyListState
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.data.WatchSessionDetail
import kotlinx.coroutines.delay

/**
 * Task detail (mockup 05): progress, the plan, and the two controls that
 * actually matter on a wrist — stop, or go back.
 *
 * Every number shown here comes from the node: the step list is the agent's own
 * todo list and the elapsed time is derived from the turn's start timestamp. The
 * screen deliberately does not estimate a percentage from anything else — an
 * invented progress bar is worse than no progress bar, because it trains the
 * user to trust a number that means nothing.
 */
@Composable
fun SessionDetailScreen(
    detail: WatchSessionDetail?,
    sessionId: String,
    loading: Boolean,
    onStop: () -> Unit,
    onBack: () -> Unit,
    onTalk: () -> Unit,
    speaking: Boolean = false,
    onSpeak: () -> Unit = {},
) {
    val listState = rememberScalingLazyListState()

    // Elapsed time ticks locally once per second. The server sends one start
    // timestamp rather than a counter, so a watch that wakes up mid-run still
    // shows the right value without polling for it.
    var elapsed by remember(detail?.startedAt) { mutableStateOf("") }
    LaunchedEffect(detail?.startedAt, detail?.running) {
        val started = detail?.startedAt
        if (started.isNullOrBlank()) {
            elapsed = ""
            return@LaunchedEffect
        }
        while (true) {
            elapsed = elapsedSince(started)
            delay(1000)
        }
    }

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier.fillMaxSize(),
    ) {
        item {
            val running = detail?.running == true
            ScreenTitle("任务详情", if (running) "运行中" else "空闲")
        }

        if (detail == null) {
            item {
                MessageBlock(
                    title = if (loading) "加载中…" else "读不到会话",
                    detail = if (loading) null else "可能已被删除",
                )
            }
        }

        detail?.let { d ->
            // Progress: only rendered when the agent actually published a plan.
            if (d.hasSteps) {
                item {
                    Column(modifier = Modifier.fillMaxWidth()) {
                        Text(
                            text = "${d.progressPercent}%",
                            style = MaterialTheme.typography.display3,
                            color = GodexColors.Accent,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Text(
                            text = "第 ${d.stepDone}/${d.stepTotal} 步",
                            style = MaterialTheme.typography.caption1,
                            color = GodexColors.TextSecondary,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(4.dp))
                        ProgressBar(percent = d.progressPercent)
                    }
                }
            }

            item {
                Column(modifier = Modifier.fillMaxWidth()) {
                    // Prefer the node's shortened reply (B4) and fall back to the raw
                    // preview: an older node does not send reply_summary, and the
                    // screen must still show something in that case.
                    val title = (d.replySummary?.takeIf { it.isNotBlank() }
                        ?: d.lastMessage?.text?.takeIf { it.isNotBlank() })
                    if (title != null) {
                        Text(
                            text = title,
                            style = MaterialTheme.typography.body1,
                            color = GodexColors.TextPrimary,
                            maxLines = 3,
                            overflow = TextOverflow.Ellipsis,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Spacer(Modifier.height(4.dp))
                    }
                    val tool = d.currentTool
                    if (!tool.isNullOrBlank()) {
                        StatusDot(color = GodexColors.Accent, label = "当前工具 · $tool")
                    } else {
                        StatusDot(
                            color = if (d.running) GodexColors.Accent else GodexColors.TextMuted,
                            label = if (d.running) "执行中" else "空闲",
                        )
                    }
                    if (elapsed.isNotEmpty()) {
                        Spacer(Modifier.height(2.dp))
                        Text(
                            text = "已用时 $elapsed",
                            style = MaterialTheme.typography.caption1,
                            color = GodexColors.TextMuted,
                            textAlign = TextAlign.Center,
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                }
            }

            if (d.hasSteps) {
                item {
                    Text(
                        text = "执行计划",
                        style = MaterialTheme.typography.caption1,
                        color = GodexColors.TextSecondary,
                        textAlign = TextAlign.Center,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                items(d.steps) { step ->
                    StepRow(
                        content = step.content,
                        marker = if (step.isDone) "●" else if (step.isActive) "◐" else "○",
                        color = when {
                            step.isDone -> GodexColors.Accent
                            step.isActive -> GodexColors.Attention
                            else -> GodexColors.TextMuted
                        },
                    )
                }
            }

            // What the agent has actually been doing (mockup 05). Ordered and
            // bounded by the server; the wrist shows a few lines rather than
            // inventing a summary of its own.
            if (d.recentTools.isNotEmpty()) {
                item {
                    Spacer(Modifier.height(4.dp))
                    Text(
                        text = "最近工具",
                        style = MaterialTheme.typography.caption1,
                        color = GodexColors.TextSecondary,
                        textAlign = TextAlign.Center,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                items(d.recentTools) { tool ->
                    ToolRow(tool)
                }
            }

            item {
                Spacer(Modifier.height(6.dp))
                Column(
                    modifier = Modifier.fillMaxWidth(),
                    verticalArrangement = Arrangement.spacedBy(ChipSpacing),
                ) {
                    // Stop is only offered when there is something to stop; a
                    // permanently visible dead button is worse than none.
                    if (d.hasRunningTurn) {
                        RoundAction(
                            label = "停止",
                            color = GodexColors.Danger,
                            onClick = onStop,
                        )
                    } else {
                        RoundAction(
                            label = "说话",
                            enabled = sessionId.isNotEmpty(),
                            onClick = onTalk,
                        )
                    }
                    // Read the reply back (B7). Enabled only when there is a reply
                    // to speak, so a tap is never a dead end; while speaking the same
                    // control stops playback instead of stacking a second clip.
                    val canSpeak = !d.replySummary.isNullOrBlank() || !d.lastMessage?.text.isNullOrBlank()
                    SmallAction(
                        label = if (speaking) "停止朗读" else "朗读回复",
                        onClick = onSpeak,
                        enabled = speaking || canSpeak,
                        modifier = Modifier.fillMaxWidth(),
                    )
                    SmallAction("返回", onBack, Modifier.fillMaxWidth())
                }
            }
        }

        if (detail == null && !loading) {
            item {
                Spacer(Modifier.height(6.dp))
                SmallAction("返回", onBack, Modifier.fillMaxWidth())
            }
        }
    }
}

/** One plan entry: marker + text, wrapped so long steps stay readable. */
@Composable
private fun StepRow(content: String, marker: String, color: Color) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.Start,
    ) {
        Text(
            text = marker,
            style = MaterialTheme.typography.caption1,
            color = color,
        )
        Spacer(Modifier.width(4.dp))
        Text(
            text = content,
            style = MaterialTheme.typography.caption1,
            color = if (color == GodexColors.TextMuted) GodexColors.TextSecondary else GodexColors.TextPrimary,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

/**
 * One tool call: marker, name, and how it went.
 *
 * The outcome is drawn as a marker rather than written as a word because a wrist
 * line is narrow, and the three states that matter to a user deciding whether to
 * intervene are "working", "done" and "failed" — not the tool's own vocabulary.
 */
@Composable
private fun ToolRow(tool: com.godex.wear.data.WatchSessionDetail.ToolEvent) {
    val color = when {
        tool.timedOut || tool.failed -> GodexColors.Danger
        tool.pending -> GodexColors.Attention
        else -> GodexColors.Accent
    }
    val marker = when {
        tool.timedOut -> "⏱"
        tool.failed -> "✕"
        tool.pending -> "◐"
        else -> "●"
    }
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = Arrangement.Start,
    ) {
        Text(
            text = marker,
            style = MaterialTheme.typography.caption1,
            color = color,
        )
        Spacer(Modifier.width(4.dp))
        Text(
            text = buildString {
                append(tool.name)
                if (tool.pending) {
                    append(" · 进行中")
                } else if (tool.durationText.isNotEmpty()) {
                    append(" · ").append(tool.durationText)
                }
            },
            style = MaterialTheme.typography.caption1,
            color = if (tool.failed || tool.timedOut) GodexColors.Danger else GodexColors.TextPrimary,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
    }
}

/**
 * Thin horizontal progress bar.
 *
 * Drawn with two Boxes rather than a Material LinearProgressIndicator because
 * Wear Material does not ship one, and pulling a phone-sized component onto a
 * circular display costs more than it saves.
 */
@Composable
private fun ProgressBar(percent: Int) {
    val clamped = percent.coerceIn(0, 100)
    androidx.compose.foundation.layout.Box(
        modifier = Modifier
            .fillMaxWidth()
            .height(4.dp)
            .clip(RoundedCornerShape(2.dp))
            .background(GodexColors.SurfaceHigh),
    ) {
        androidx.compose.foundation.layout.Box(
            modifier = Modifier
                .fillMaxWidth(clamped / 100f)
                .height(4.dp)
                .clip(RoundedCornerShape(2.dp))
                .background(GodexColors.Accent),
        )
    }
}

/** Formats a duration as `12 分 04 秒`, matching the mockup's reading order. */
internal fun elapsedSince(startedIso: String, now: java.time.Instant = java.time.Instant.now()): String {
    val started = runCatching { java.time.Instant.parse(startedIso) }.getOrElse {
        runCatching { java.time.OffsetDateTime.parse(startedIso).toInstant() }.getOrNull()
    } ?: return ""
    val total = java.time.Duration.between(started, now).seconds.coerceAtLeast(0)
    val minutes = total / 60
    val seconds = total % 60
    return when {
        minutes >= 60 -> "${minutes / 60} 小时 ${minutes % 60} 分"
        minutes > 0 -> "$minutes 分 ${"%02d".format(seconds)} 秒"
        else -> "$seconds 秒"
    }
}
