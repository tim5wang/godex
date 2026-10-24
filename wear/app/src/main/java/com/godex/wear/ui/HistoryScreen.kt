package com.godex.wear.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.foundation.lazy.ScalingLazyColumn
import androidx.wear.compose.foundation.lazy.items
import androidx.wear.compose.foundation.lazy.rememberScalingLazyListState
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R
import com.godex.wear.data.WatchHistoryMessage

/**
 * Conversation history (multi-turn).
 *
 * The detail screen shows only the newest reply, which left no way to see what
 * was said before it — on a remote control that is the difference between "I
 * remember the context" and "I have to go find a phone". This screen scrolls back
 * through the turns the node keeps for the session.
 *
 * Deliberately read-only and bounded: a wrist has no room to compose, edit or
 * search, and re-rendering an entire long conversation would both be slow to
 * scroll and cost a network round trip per open. Tapping a message opens the
 * full text, the same reader the detail screen uses for long replies.
 */
@Composable
fun HistoryScreen(
    messages: List<WatchHistoryMessage>,
    loading: Boolean,
    onBack: () -> Unit,
) {
    val listState = rememberScalingLazyListState()

    // Same reader as the detail screen, kept as screen-local state: the open
    // message belongs to this screen, and hoisting it to the root would leave a
    // full-text view bound to a list the user already navigated away from.
    var fullText by remember { mutableStateOf<String?>(null) }
    val openText = fullText
    if (openText != null) {
        FullTextScreen(text = openText, onBack = { fullText = null })
        return
    }

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier.fillMaxSize(),
    ) {
        item {
            // The count tells the user how far back this goes before they scroll,
            // so "only three turns" is not mistaken for "history is broken".
            ScreenTitle(
                stringResource(R.string.history_title),
                if (messages.isEmpty()) null
                else stringResource(R.string.history_count, messages.size),
            )
        }

        if (loading && messages.isEmpty()) {
            item {
                MessageBlock(
                    title = stringResource(R.string.common_loading),
                    detail = stringResource(R.string.history_loading_detail),
                )
            }
        } else if (messages.isEmpty()) {
            // Not an error: a session that has not been talked to yet has no
            // history, and reporting that as a failure would be misleading.
            item {
                MessageBlock(
                    title = stringResource(R.string.history_empty),
                    detail = stringResource(R.string.history_empty_detail),
                )
            }
        }

        items(messages) { message ->
            HistoryBubble(message = message, onOpenFullText = { fullText = it })
        }

        item {
            Spacer(Modifier.height(6.dp))
            SmallAction(
                stringResource(R.string.common_back),
                onBack,
                Modifier.fillMaxWidth(),
            )
        }
    }
}

/**
 * One turn.
 *
 * Role is shown as a short label rather than by alignment or colour alone: on a
 * round monochrome-ish display, "me" versus the agent has to survive a glance in
 * sunlight, and the label also carries through to screen readers.
 */
@Composable
private fun HistoryBubble(message: WatchHistoryMessage, onOpenFullText: (String) -> Unit) {
    val isUser = message.isUser
    Column(
        modifier = Modifier
            .fillMaxWidth()
            .background(
                if (isUser) GodexColors.SurfaceHigh else GodexColors.Surface,
            )
            .padding(horizontal = 8.dp, vertical = 6.dp)
            // Truncated messages must be openable, otherwise the tail of a long
            // reply is unreachable — the reason this screen exists in the first
            // place. Non-truncated messages are not clickable: a tap that opens
            // the same text it just showed is a dead end.
            .then(
                if (message.truncated) Modifier.clickable { onOpenFullText(message.text) }
                else Modifier,
            ),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            text = if (isUser) stringResource(R.string.history_you)
            else stringResource(R.string.history_agent),
            style = MaterialTheme.typography.caption1,
            color = if (isUser) GodexColors.Accent else GodexColors.TextSecondary,
            textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth(),
        )
        Spacer(Modifier.height(2.dp))
        Text(
            text = message.text,
            style = MaterialTheme.typography.body2,
            color = GodexColors.TextPrimary,
            textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth(),
        )
        if (message.truncated) {
            Spacer(Modifier.height(2.dp))
            Text(
                text = stringResource(R.string.history_truncated),
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}
