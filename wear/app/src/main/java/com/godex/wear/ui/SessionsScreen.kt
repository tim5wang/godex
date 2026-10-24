package com.godex.wear.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
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
import androidx.wear.compose.material.CompactChip
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R
import com.godex.wear.data.WatchSession
import com.godex.wear.data.WatchTemplate

/**
 * Session switcher (mockups 04 / 13).
 *
 * Uses [ScalingLazyColumn] rather than a plain list: Wear OS scales items toward
 * the top and bottom of a round screen, which keeps the focused row readable and
 * avoids text clipped by the bezel.
 *
 * Pinned sessions are hoisted into their own group above the rest (mockup 13).
 * Pinning is local to the watch — a personal shortcut, not a property of the
 * session — which is why it works offline and is never sent to the node.
 *
 * **Deviation from mockup 13:** the mockup specifies long-press to pin, but Wear
 * Compose 1.4's `Chip` exposes no onLongClick (verified against the compiled
 * artifact), and a nested combinedClickable would fight the chip's own gesture
 * handling. Each row therefore carries an explicit pin toggle. That also makes
 * the affordance discoverable, which a hidden long-press is not.
 */
@Composable
fun SessionsScreen(
    sessions: List<WatchSession>,
    currentSessionId: String,
    pinned: Set<String>,
    loading: Boolean,
    templates: List<WatchTemplate> = emptyList(),
    onSelect: (String) -> Unit,
    onTogglePin: (String) -> Unit,
    onCrossDevice: () -> Unit,
    onCreate: (String) -> Unit = {},
    onOpenCreate: () -> Unit = {},
    onBack: () -> Unit,
) {
    val listState = rememberScalingLazyListState()

    // Creating a conversation is a mode of this screen rather than a separate
    // destination: it is a two-tap action from a list of at most three presets, and
    // a whole screen for it would be more navigation than decision.
    var showCreate by remember { mutableStateOf(false) }

    val pinnedSessions = sessions.filter { pinned.contains(it.sessionId) }
    val restSessions = sessions.filterNot { pinned.contains(it.sessionId) }

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier.fillMaxSize(),
    ) {
        item {
            ScreenTitle(
                stringResource(R.string.common_session),
                stringResource(R.string.sessions_count, sessions.size),
            )
        }

        if (showCreate) {
            // One tap per preset. No confirm step: the user can create another one in
            // the same number of taps, so a wrong choice costs nothing.
            item {
                Text(
                    text = stringResource(R.string.sessions_pick_preset),
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
            items(templates, key = { "tpl-${it.id}" }) { tpl ->
                Chip(
                    label = {
                        Text(
                            text = if (tpl.avatar.isBlank()) tpl.name else "${tpl.avatar} ${tpl.name}",
                            style = MaterialTheme.typography.body1,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                    },
                    secondaryLabel = {
                        if (tpl.description.isNotBlank()) {
                            Text(
                                text = tpl.description,
                                style = MaterialTheme.typography.caption1,
                                maxLines = 2,
                                overflow = TextOverflow.Ellipsis,
                            )
                        }
                    },
                    onClick = {
                        showCreate = false
                        onCreate(tpl.id)
                    },
                    colors = ChipDefaults.secondaryChipColors(
                        backgroundColor = GodexColors.Surface,
                        contentColor = GodexColors.TextPrimary,
                    ),
                    modifier = Modifier.fillMaxWidth(),
                )
            }
            item {
                Spacer(Modifier.height(ChipSpacing))
                SmallAction(
                    label = stringResource(R.string.common_cancel),
                    onClick = { showCreate = false },
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        } else {
            item {
                SmallAction(
                    label = stringResource(R.string.sessions_new),
                    highlight = true,
                    onClick = {
                        onOpenCreate()
                        showCreate = true
                    },
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        if (sessions.isEmpty()) {
            item {
                MessageBlock(
                    title = if (loading) stringResource(R.string.common_loading)
                    else stringResource(R.string.common_no_sessions),
                    detail = if (loading) null else stringResource(R.string.home_empty_detail),
                )
            }
        }

        if (pinnedSessions.isNotEmpty()) {
            item { GroupLabel(stringResource(R.string.sessions_pinned)) }
            items(pinnedSessions, key = { "pin-${it.sessionId}" }) { session ->
                SessionRow(
                    session = session,
                    selected = session.sessionId == currentSessionId,
                    pinned = true,
                    onSelect = onSelect,
                    onTogglePin = onTogglePin,
                )
            }
        }

        if (restSessions.isNotEmpty()) {
            if (pinnedSessions.isNotEmpty()) {
                item { GroupLabel(stringResource(R.string.common_all)) }
            }
            items(restSessions, key = { it.sessionId }) { session ->
                SessionRow(
                    session = session,
                    selected = session.sessionId == currentSessionId,
                    pinned = false,
                    onSelect = onSelect,
                    onTogglePin = onTogglePin,
                )
            }
        }

        item {
            Spacer(Modifier.height(6.dp))
            Column(modifier = Modifier.fillMaxWidth()) {
                SmallAction(
                    label = stringResource(R.string.sessions_cross_device),
                    onClick = onCrossDevice,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(ChipSpacing))
                SmallAction(
                    label = stringResource(R.string.common_back),
                    onClick = onBack,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
    }
}

@Composable
private fun SessionRow(
    session: WatchSession,
    selected: Boolean,
    pinned: Boolean,
    onSelect: (String) -> Unit,
    onTogglePin: (String) -> Unit,
) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        verticalAlignment = Alignment.CenterVertically,
    ) {
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
                val mark = when {
                    session.running -> stringResource(R.string.common_running)
                    selected -> stringResource(R.string.common_current)
                    else -> ""
                }
                val parts = listOfNotNull(mark.takeIf { it.isNotEmpty() }, when_.takeIf { it.isNotEmpty() })
                if (parts.isNotEmpty()) {
                    Text(
                        text = parts.joinToString(" · "),
                        style = MaterialTheme.typography.caption1,
                        maxLines = 1,
                    )
                }
            },
            onClick = { onSelect(session.sessionId) },
            colors = ChipDefaults.secondaryChipColors(
                // The active session is tinted so the user can see which
                // conversation the remote control is pointed at.
                backgroundColor = if (selected) GodexColors.Accent else GodexColors.Surface,
                contentColor = if (selected) Color.Black else GodexColors.TextPrimary,
            ),
            modifier = Modifier.weight(1f),
        )
        Spacer(Modifier.width(4.dp))
        // Separate tap target, so pinning never fires by accident while the
        // user is trying to switch sessions.
        CompactChip(
            onClick = { onTogglePin(session.sessionId) },
            label = { Text(if (pinned) "★" else "☆") },
            colors = ChipDefaults.secondaryChipColors(
                backgroundColor = if (pinned) GodexColors.Attention else GodexColors.SurfaceHigh,
                contentColor = if (pinned) Color.Black else GodexColors.TextMuted,
            ),
        )
    }
}

/** Small section divider used to separate the pinned group from the rest. */
@Composable
private fun GroupLabel(text: String) {
    Text(
        text = text,
        style = MaterialTheme.typography.caption1,
        color = GodexColors.TextMuted,
        modifier = Modifier.fillMaxWidth(),
    )
}
