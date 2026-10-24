package com.godex.wear.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.wear.compose.foundation.lazy.ScalingLazyColumn
import androidx.wear.compose.foundation.lazy.items
import androidx.wear.compose.foundation.lazy.rememberScalingLazyListState
import androidx.wear.compose.material.Chip
import androidx.wear.compose.material.ChipDefaults
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.data.WatchCrossDeviceSession

/**
 * Cross-device unified list (mockup 14).
 *
 * Filtering is a *view*, not a mode: it narrows what is shown and never changes
 * which session commands go to. That distinction is the whole reason this screen
 * can be a pure client-side concern — the server returns one unfiltered list and
 * the watch narrows it locally, so tapping a filter costs no round-trip.
 *
 * A node that is offline contributes no sessions (the center does not cache a
 * peer's history), and its missing sessions are reported rather than silently
 * dropped, so an empty list is never mistaken for "no work anywhere".
 */
@Composable
fun CrossDeviceScreen(
    sessions: List<WatchCrossDeviceSession>,
    currentSessionId: String,
    loading: Boolean,
    onSelect: (String) -> Unit,
    onBack: () -> Unit,
) {
    val listState = rememberScalingLazyListState()

    // "全部" plus one entry per node actually present, derived from the data so
    // the filter row never offers an empty choice.
    val nodeFilters = remember(sessions) {
        sessions.map { it.nodeId to it.nodeName.ifBlank { it.nodeId } }
            .distinct()
            .sortedBy { it.second }
    }
    var selectedNode by remember { mutableStateOf<String?>(null) }

    // A node can disappear between refreshes; falling back to "全部" keeps the
    // list from appearing empty because of a stale filter.
    val activeFilter = selectedNode?.takeIf { id -> nodeFilters.any { it.first == id } }
    val visible = if (activeFilter == null) sessions else sessions.filter { it.nodeId == activeFilter }

    val offlineNodes = remember(sessions) {
        sessions.filter { !it.online }.map { it.nodeName.ifBlank { it.nodeId } }.distinct()
    }

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier.fillMaxSize(),
    ) {
        item {
            ScreenTitle("全部任务", "${visible.size} 个 · ${nodeFilters.size} 端")
        }

        if (nodeFilters.size > 1) {
            item {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    FilterChip(
                        label = "全部",
                        selected = activeFilter == null,
                        onClick = { selectedNode = null },
                        modifier = Modifier.weight(1f),
                    )
                    // A wrist shows a couple of filters at most; the rest stay
                    // reachable by scrolling the row horizontally.
                    nodeFilters.take(2).forEach { (id, name) ->
                        FilterChip(
                            label = name,
                            selected = activeFilter == id,
                            onClick = { selectedNode = id },
                            modifier = Modifier.weight(1f),
                        )
                    }
                }
            }
        }

        if (offlineNodes.isNotEmpty()) {
            item {
                Text(
                    text = "${offlineNodes.joinToString("、")} 离线，读不到它的会话（中心不缓存历史）",
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        if (visible.isEmpty()) {
            item {
                MessageBlock(
                    title = if (loading) "加载中…" else "没有会话",
                    detail = if (loading) null else "所有已知设备都没有活跃任务",
                )
            }
        }

        items(visible, key = { "${it.nodeId}-${it.sessionId}" }) { session ->
            val selected = session.sessionId == currentSessionId
            Chip(
                label = {
                    Text(
                        text = session.title.ifBlank { "未命名会话" },
                        style = MaterialTheme.typography.body1,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                },
                secondaryLabel = {
                    val when_ = relativeTime(session.lastActivityAt)
                    val parts = listOfNotNull(
                        session.nodeName.ifBlank { session.nodeId }.takeIf { it.isNotEmpty() },
                        (if (!session.online) "离线" else if (session.running) "运行中" else "")
                            .takeIf { it.isNotEmpty() },
                        when_.takeIf { it.isNotEmpty() },
                    )
                    if (parts.isNotEmpty()) {
                        Text(
                            text = parts.joinToString(" · "),
                            style = MaterialTheme.typography.caption1,
                            maxLines = 1,
                        )
                    }
                },
                // A remote session is shown for awareness but cannot be driven
                // from here: the remote-control credential is scoped to this node,
                // so pretending otherwise would fail at the first command.
                onClick = { if (!session.remote) onSelect(session.sessionId) },
                colors = ChipDefaults.secondaryChipColors(
                    backgroundColor = when {
                        selected -> GodexColors.Accent
                        session.remote -> GodexColors.SurfaceHigh
                        else -> GodexColors.Surface
                    },
                    contentColor = when {
                        selected -> Color.Black
                        session.remote -> GodexColors.TextSecondary
                        else -> GodexColors.TextPrimary
                    },
                ),
                modifier = Modifier.fillMaxWidth(),
            )
        }

        item {
            Spacer(Modifier.height(6.dp))
            SmallAction(label = "返回", onClick = onBack, modifier = Modifier.fillMaxWidth())
        }
    }
}

/**
 * Compact filter pill; the selected one is tinted to read as "current view".
 *
 * The modifier is passed in (rather than applying `weight` internally) because
 * `RowScope` is only available at the call site.
 */
@Composable
private fun FilterChip(
    label: String,
    selected: Boolean,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Chip(
        label = {
            Text(
                text = label,
                style = MaterialTheme.typography.caption1,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        },
        onClick = onClick,
        colors = ChipDefaults.secondaryChipColors(
            backgroundColor = if (selected) GodexColors.Accent else GodexColors.SurfaceHigh,
            contentColor = if (selected) Color.Black else GodexColors.TextSecondary,
        ),
        modifier = modifier,
    )
}
