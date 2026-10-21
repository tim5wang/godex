package com.godex.wear.ui

import androidx.compose.foundation.background
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
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.Chip
import androidx.wear.compose.material.ChipDefaults
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text

/**
 * Shared building blocks for the round-screen layouts.
 *
 * Kept in one file (rather than per-screen) so the round-screen spacing rules
 * are defined exactly once: on a 454px circular display, a component that works
 * on a rectangular phone silently clips at the edges.
 */

/** Screen scaffold with the padding a round display actually needs. */
@Composable
fun RoundScreen(content: @Composable () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(GodexColors.Background)
            .padding(horizontal = 12.dp),
        contentAlignment = Alignment.Center,
    ) {
        content()
    }
}

/** Centered title used at the top of every non-list screen. */
@Composable
fun ScreenTitle(text: String, subtitle: String? = null) {
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        Text(
            text = text,
            style = MaterialTheme.typography.title2,
            color = GodexColors.TextPrimary,
            textAlign = TextAlign.Center,
        )
        if (!subtitle.isNullOrBlank()) {
            Spacer(Modifier.height(2.dp))
            Text(
                text = subtitle,
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextSecondary,
                textAlign = TextAlign.Center,
            )
        }
    }
}

/**
 * Large circular action button.
 *
 * `TonalChip` is not used here because the primary actions on a watch must be
 * sized for a fingertip on a moving wrist, not for a mouse pointer.
 */
@Composable
fun RoundAction(
    label: String,
    onClick: () -> Unit,
    color: Color = GodexColors.Accent,
    enabled: Boolean = true,
    modifier: Modifier = Modifier,
) {
    Chip(
        label = {
            Text(
                text = label,
                style = MaterialTheme.typography.button,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        },
        onClick = onClick,
        enabled = enabled,
        colors = ChipDefaults.primaryChipColors(
            backgroundColor = if (enabled) color else GodexColors.SurfaceHigh,
            contentColor = if (enabled) Color.Black else GodexColors.TextMuted,
        ),
        modifier = modifier.fillMaxWidth(),
    )
}

/** Small status dot + label; the fastest way to read state at a glance. */
@Composable
fun StatusDot(color: Color, label: String) {
    androidx.compose.foundation.layout.Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.Center,
    ) {
        Box(
            modifier = Modifier
                .size(7.dp)
                .clip(CircleShape)
                .background(color),
        )
        Spacer(Modifier.size(5.dp))
        Text(
            text = label,
            style = MaterialTheme.typography.caption1,
            color = GodexColors.TextSecondary,
        )
    }
}

/** Consistent error / empty presentation so screens do not invent their own. */
@Composable
fun MessageBlock(title: String, detail: String? = null, tint: Color = GodexColors.TextSecondary) {
    Column(
        modifier = Modifier.fillMaxWidth(),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            text = title,
            style = MaterialTheme.typography.body1,
            color = tint,
            textAlign = TextAlign.Center,
        )
        if (!detail.isNullOrBlank()) {
            Spacer(Modifier.height(4.dp))
            Text(
                text = detail,
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
            )
        }
    }
}
