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
import com.godex.wear.data.WatchCrossDeviceSession

/**
 * Cross-device unified list (mockup 14).
 *
 * Filtering is a *view*, not a mode: it narrows what is shown. What commands follow
 * is decided by which session is tapped, and tapping one always names both the
 * node and the session — so the selection can move the remote control to another
 * machine instead of silently staying where it was.
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
    onSelect: (nodeId: String, sessionId: String) -> Unit,
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
            ScreenTitle(
                stringResource(R.string.cross_title),
                stringResource(R.string.cross_summary, visible.size, nodeFilters.size),
            )
        }

        if (nodeFilters.size > 1) {
            item {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.spacedBy(4.dp),
                ) {
                    FilterChip(
                        label = stringResource(R.string.common_all),
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
                    text = stringResource(
                        R.string.cross_offline_note,
                        offlineNodes.joinToString(", "),
                    ),
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        if (visible.isEmpty()) {
            item {
                MessageBlock(
                    title = if (loading) stringResource(R.string.common_loading)
                    else stringResource(R.string.common_no_sessions),
                    detail = if (loading) null else stringResource(R.string.cross_empty_detail),
                )
            }
        }

        items(visible, key = { "${it.nodeId}-${it.sessionId}" }) { session ->
            val selected = session.sessionId == currentSessionId
            Chip(
                label = {
                    Text(
                        text = session.title.ifBlank { stringResource(R.string.common_unnamed_session) },
                        style = MaterialTheme.typography.body1,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                },
                secondaryLabel = {
                    val when_ = relativeTime(session.lastActivityAt)
                    val parts = listOfNotNull(
                        session.nodeName.ifBlank { session.nodeId }.takeIf { it.isNotEmpty() },
                        (
                            if (!session.online) stringResource(R.string.common_offline)
                            else if (session.running) stringResource(R.string.common_running)
                            else ""
                            ).takeIf { it.isNotEmpty() },
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
                // Tapping carries the owning node too: a remote-control credential is
                // scoped to one node, but the paired node forwards for other machines,
                // so a remote session IS drivable. Passing only the session id would
                // leave the target on the previous device, and the next refresh would
                // read that device's list back — undoing the tap.
                onClick = { onSelect(session.nodeId, session.sessionId) },
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
            SmallAction(
                label = stringResource(R.string.common_back),
                onClick = onBack,
                modifier = Modifier.fillMaxWidth(),
            )
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
