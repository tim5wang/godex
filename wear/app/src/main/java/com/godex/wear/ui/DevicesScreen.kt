package com.godex.wear.ui

import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
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
import com.godex.wear.data.WatchNode

/**
 * Node list (mockups 09 / 14).
 *
 * This is the "which computer am I driving?" screen. Selecting a node only
 * re-points the remote control; it does not move a session, which is why the
 * selection is local state rather than a server call.
 */
@Composable
fun DevicesScreen(
    nodes: List<WatchNode>,
    targetDeviceId: String,
    loading: Boolean,
    onSelect: (String) -> Unit,
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
                stringResource(R.string.devices_title),
                stringResource(R.string.devices_count, nodes.size),
            )
        }

        if (nodes.isEmpty()) {
            item {
                MessageBlock(
                    title = if (loading) stringResource(R.string.common_loading)
                    else stringResource(R.string.devices_empty),
                    detail = if (loading) null else stringResource(R.string.devices_empty_detail),
                )
            }
        }

        items(nodes, key = { it.id }) { node ->
            val selected = node.id == targetDeviceId
            Chip(
                label = {
                    Text(
                        text = node.name.ifBlank { node.id },
                        style = MaterialTheme.typography.body1,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                },
                secondaryLabel = {
                    val when_ = relativeTime(node.lastSeen)
                    Text(
                        text = buildString {
                            append(
                                if (node.isOnline) stringResource(R.string.common_online)
                                else stringResource(R.string.common_offline),
                            )
                            // What the target is matters more than when it was last
                            // seen: "which computer am I driving?" is this screen's
                            // question, and a phone is not a desktop.
                            nodeKindLabel(node.kind).takeIf { it.isNotEmpty() }
                                ?.let { append(" · ").append(it) }
                            if (selected) append(stringResource(R.string.devices_current))
                            when_.takeIf { it.isNotEmpty() }?.let { append(" · ").append(it) }
                        },
                        style = MaterialTheme.typography.caption1,
                        maxLines = 1,
                    )
                },
                onClick = { onSelect(node.id) },
                colors = ChipDefaults.secondaryChipColors(
                    backgroundColor = if (selected) GodexColors.Accent else GodexColors.Surface,
                    contentColor = if (selected) Color.Black else GodexColors.TextPrimary,
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
