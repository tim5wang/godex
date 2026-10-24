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
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text

/**
 * Push-to-talk (mockup 07), with a typed fallback.
 *
 * Press-and-hold is the primary interaction because it is the only one that works
 * reliably with a gloved or moving hand — but typing stays available for quiet
 * settings, so the screen never becomes a dead end when dictation fails.
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
                text = if (recording) "松开发送" else "按住说话",
                subtitle = sessionTitle.ifBlank { "未选择会话" },
            )

            Spacer(Modifier.height(12.dp))

            if (showKeyboard) {
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
                    label = "发送",
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
                    text = if (recording) "正在录音" else "按住开始 · 拖开取消",
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
                    label = if (showKeyboard) "改回语音" else "改打字",
                    onClick = { showKeyboard = !showKeyboard },
                    modifier = Modifier.fillMaxWidth(),
                )
                SmallAction(label = "返回", onClick = onBack, modifier = Modifier.fillMaxWidth())
            }
        }
    }
}
