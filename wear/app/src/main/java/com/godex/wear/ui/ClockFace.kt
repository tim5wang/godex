package com.godex.wear.ui

import android.content.Context
import android.os.BatteryManager
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import kotlinx.coroutines.delay
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Clock face: the system information a watch is expected to show at a glance.
 *
 * This is an in-app face, not a registered system watch face. On this device
 * (Xiaomi Watch 5, MIWEAR) a third-party face cannot be installed at all — the
 * standard Wear entry point answers "No activities found" for
 * `com.google.android.wearable.action.WATCH_FACE`, and Xiaomi's own faces run as
 * `WallpaperService`s whose control surface is gated behind the privileged
 * permission `com.xiaomi.wear.permission.watchface.DWF_RUNTIME_PRIVILEGED`.
 * The measured evidence is in docs/wearos-watchface-capability.md.
 *
 * So the time is rendered here, at the top of the app's home screen, where a
 * wrist glance lands after opening GoDex. The system-level surface that *is*
 * available to us is the Tile, which already shows the agent's state.
 *
 * Formatting comes from the platform formatters rather than from a hard-coded
 * pattern: `DateFormat.getTimeFormat` honours the 12/24-hour setting the user
 * chose in system settings, and the date/weekday formatters honour the locale,
 * so a watch set to English does not show "10月25日".
 */
@Composable
fun ClockFace(modifier: Modifier = Modifier) {
    val context = LocalContext.current
    // Locale-sensitive, and re-read on locale change so switching the watch
    // language does not leave the date in the previous one.
    val locale = Locale.getDefault()
    val timeFormat = remember(locale) { android.text.format.DateFormat.getTimeFormat(context) }
    val dateFormat = remember(locale) { SimpleDateFormat("MMM d", locale) }
    val weekdayFormat = remember(locale) { SimpleDateFormat("EEE", locale) }

    var time by remember(locale) { mutableStateOf(timeFormat.format(Date())) }
    var date by remember(locale) { mutableStateOf("") }
    var battery by remember(locale) { mutableStateOf(-1) }

    LaunchedEffect(locale) {
        // Ticks once a second but only publishes a change when the rendered text
        // actually differs. The face shows minutes, so a naive per-second state
        // write would recompose this subtree 60 times for every visible change —
        // on a watch that is pure battery cost for no visual difference.
        var ticks = 0
        while (true) {
            val now = Date()
            val nextTime = timeFormat.format(now)
            if (nextTime != time) time = nextTime

            val nextDate = dateFormat.format(now) + " · " + weekdayFormat.format(now)
            if (nextDate != date) date = nextDate

            // Battery changes on the order of minutes; polling it every second
            // would be pointless binder traffic.
            if (ticks % 30 == 0) {
                val level = batteryPercent(context)
                if (level != battery) battery = level
            }
            ticks++

            delay(1000L)
        }
    }

    Column(
        modifier = modifier.fillMaxWidth(),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            text = time,
            style = MaterialTheme.typography.title1,
            color = GodexColors.TextPrimary,
            textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth(),
        )
        if (date.isNotEmpty()) {
            Spacer(Modifier.height(2.dp))
            Text(
                text = if (battery >= 0) "$date · $battery%" else date,
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

/**
 * Battery percentage, or -1 when the platform cannot report it.
 *
 * -1 rather than 0: an unreadable battery must disappear from the face, not claim
 * the watch is empty.
 */
private fun batteryPercent(context: Context): Int {
    val manager = context.getSystemService(Context.BATTERY_SERVICE) as? BatteryManager
        ?: return -1
    val level = runCatching {
        manager.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY)
    }.getOrDefault(-1)
    return if (level in 0..100) level else -1
}
