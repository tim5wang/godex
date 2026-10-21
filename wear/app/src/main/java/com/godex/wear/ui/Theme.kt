package com.godex.wear.ui

import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.Colors
import androidx.wear.compose.material.MaterialTheme
import androidx.compose.runtime.Composable

/**
 * Dark-first palette.
 *
 * A watch is worn all day, so the default theme is near-black: on an OLED panel
 * black pixels are simply off, which is the single largest display power saving
 * available. Accents are used only to convey state, never decoration.
 */
object GodexColors {
    val Background = Color(0xFF000000)
    val Surface = Color(0xFF14161A)
    val SurfaceHigh = Color(0xFF1E2127)

    /** Running / healthy. */
    val Accent = Color(0xFF4ADE80)

    /** Needs the user (approval pending). */
    val Attention = Color(0xFFFBBF24)

    /** Failed / offline. */
    val Danger = Color(0xFFF87171)

    val TextPrimary = Color(0xFFECEFF4)
    val TextSecondary = Color(0xFF9AA3AF)

    /** Muted text that still meets contrast on black. */
    val TextMuted = Color(0xFF6B7280)
}

val ScreenPadding = 8.dp
val ChipSpacing = 6.dp

@Composable
fun GodexWearTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colors = Colors(
            primary = GodexColors.Accent,
            onPrimary = Color.Black,
            secondary = GodexColors.Attention,
            onSecondary = Color.Black,
            background = GodexColors.Background,
            onBackground = GodexColors.TextPrimary,
            surface = GodexColors.Surface,
            onSurface = GodexColors.TextPrimary,
            error = GodexColors.Danger,
            onError = Color.Black,
        ),
        content = content,
    )
}

/** Relative "x 分钟前" rendering; a wrist glance cares about recency, not clocks. */
fun relativeTime(iso: String?): String {
    val parsed = parseInstant(iso) ?: return ""

    val minutes = java.time.Duration.between(parsed, java.time.Instant.now()).toMinutes()
    return when {
        minutes < 1 -> "刚刚"
        minutes < 60 -> "${minutes} 分钟前"
        minutes < 60 * 24 -> "${minutes / 60} 小时前"
        else -> "${minutes / (60 * 24)} 天前"
    }
}

/**
 * Countdown to a deadline, e.g. "14 分钟后超时".
 *
 * Approvals expire (the server sends expires_at) and how long is left changes
 * whether the request is worth acting on from the wrist at all: one about to time
 * out is better handled natively than answered with seconds to spare. Returns
 * empty for an unparseable or already-passed deadline rather than rendering a
 * negative duration.
 */
fun remainingTime(iso: String?): String {
    val deadline = parseInstant(iso) ?: return ""
    val minutes = java.time.Duration.between(java.time.Instant.now(), deadline).toMinutes()
    return when {
        minutes < 0 -> ""
        minutes < 1 -> "即将超时"
        minutes < 60 -> "${minutes} 分钟后超时"
        minutes < 60 * 24 -> "${minutes / 60} 小时后超时"
        else -> "${minutes / (60 * 24)} 天后超时"
    }
}

/**
 * Parses a server timestamp.
 *
 * Go marshals time.Time with fractional seconds and an offset, which
 * Instant.parse alone rejects, so the offset form is tried before giving up.
 */
private fun parseInstant(iso: String?): java.time.Instant? {
    if (iso.isNullOrBlank()) return null
    return runCatching { java.time.Instant.parse(iso) }.getOrElse {
        runCatching { java.time.OffsetDateTime.parse(iso).toInstant() }.getOrNull()
    }
}
