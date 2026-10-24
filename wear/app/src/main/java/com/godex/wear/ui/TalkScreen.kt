package com.godex.wear.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.waitForUpOrCancellation
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R

/**
 * Push-to-talk (mockup 07), with a typed fallback.
 *
 * Press-and-hold is the primary interaction because it is the only one that works
 * reliably with a gloved or moving hand — but typing stays available for quiet
 * settings, so the screen never becomes a dead end when dictation fails.
 *
 * Quick phrases sit alongside both. They exist because the most common things a
 * user says to an agent on a wrist are one or two words, and saying them out loud
 * is slower and less reliable than one tap — especially on a noisy street or when
 * the node's ASR is unavailable.
 */
@Composable
fun TalkScreen(
    sessionTitle: String,
    recording: Boolean,
    onStartRecording: () -> Boolean,
    onStopRecording: () -> Unit,
    onCancelRecording: () -> Unit,
    onSendText: (String) -> Unit,
    onBack: () -> Unit,
) {
    var typed by remember { mutableStateOf("") }
    var showKeyboard by remember { mutableStateOf(false) }
    var showPhrases by remember { mutableStateOf(false) }

    RoundScreen {
        // Scrollable for the same reason as Home: on a 227dp round display this
        // content is taller than the viewport, and an overflowing Column never lays
        // out the children past the bottom edge — 返回 was absent from the view
        // hierarchy, leaving the system back gesture as the only way off the screen.
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .verticalScroll(rememberScrollState()),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            ScreenTitle(
                text = if (recording) stringResource(R.string.talk_release)
                else stringResource(R.string.talk_hold),
                subtitle = sessionTitle.ifBlank { stringResource(R.string.talk_no_session) },
            )

            Spacer(Modifier.height(12.dp))

            if (showPhrases) {
                // Sending is immediate: a two-tap flow (pick, then confirm) would be
                // slower than simply saying it, which defeats the point of a shortcut.
                Column(
                    modifier = Modifier.fillMaxWidth(),
                    verticalArrangement = Arrangement.spacedBy(ChipSpacing),
                ) {
                    QuickPhrases.forEach { phraseRes ->
                        val phrase = stringResource(phraseRes)
                        SmallAction(
                            label = phrase,
                            highlight = true,
                            onClick = {
                                onSendText(phrase)
                                showPhrases = false
                            },
                            modifier = Modifier.fillMaxWidth(),
                        )
                    }
                }
            } else if (showKeyboard) {
                BasicTextField(
                    value = typed,
                    onValueChange = { typed = it },
                    textStyle = TextStyle(color = GodexColors.TextPrimary),
                    cursorBrush = SolidColor(GodexColors.Accent),
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(GodexColors.Surface)
                        .padding(10.dp),
                )
                Spacer(Modifier.height(8.dp))
                SmallAction(
                    label = stringResource(R.string.talk_send),
                    highlight = typed.isNotBlank(),
                    onClick = {
                        if (typed.isNotBlank()) {
                            onSendText(typed.trim())
                            typed = ""
                            showKeyboard = false
                        }
                    },
                    modifier = Modifier.fillMaxWidth(),
                )
            } else {
                // The big circle is the entire interaction surface: press starts
                // capture, release sends. Cancelling is handled by dragging out,
                // which is the platform-standard escape gesture.
                Box(
                    modifier = Modifier
                        .size(110.dp)
                        .clip(CircleShape)
                        .background(if (recording) GodexColors.Danger else GodexColors.Accent)
                        .pointerInput(Unit) {
                            awaitEachGesture {
                                awaitFirstDown(requireUnconsumed = false)
                                if (!onStartRecording()) return@awaitEachGesture
                                // waitForUpOrCancellation returns null when the
                                // gesture is cancelled (finger dragged away),
                                // which maps naturally onto "discard".
                                val up = waitForUpOrCancellation()
                                if (up == null) onCancelRecording() else onStopRecording()
                            }
                        },
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = if (recording) "●" else "🎤",
                        style = MaterialTheme.typography.title1,
                        color = Color.Black,
                    )
                }
                Spacer(Modifier.height(8.dp))
                Text(
                    text = if (recording) stringResource(R.string.talk_recording)
                    else stringResource(R.string.talk_hint),
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    textAlign = TextAlign.Center,
                )
            }

            Spacer(Modifier.height(10.dp))
            Column(
                modifier = Modifier.fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy(ChipSpacing),
            ) {
                SmallAction(
                    label = if (showKeyboard) stringResource(R.string.talk_to_voice)
                    else stringResource(R.string.talk_to_text),
                    onClick = {
                        showKeyboard = !showKeyboard
                        showPhrases = false
                    },
                    modifier = Modifier.fillMaxWidth(),
                )
                SmallAction(
                    label = if (showPhrases) stringResource(R.string.talk_phrases_hide)
                    else stringResource(R.string.talk_phrases),
                    onClick = {
                        showPhrases = !showPhrases
                        showKeyboard = false
                    },
                    modifier = Modifier.fillMaxWidth(),
                )
                SmallAction(
                    label = stringResource(R.string.common_back),
                    onClick = onBack,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
    }
}

/**
 * One-tap phrases for the things a user says to an agent most often.
 *
 * Kept short and imperative so they read as commands rather than as options, and
 * fixed rather than configurable: four presets fit on a wrist, and a settings
 * screen for editing them would cost more taps than typing the phrase.
 */
private val QuickPhrases = listOf(
    R.string.phrase_ok,
    R.string.phrase_plan,
    R.string.phrase_continue,
    R.string.phrase_continue_rest,
)
