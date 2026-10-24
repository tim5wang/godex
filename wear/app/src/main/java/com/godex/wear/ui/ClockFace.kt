package com.godex.wear.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R
import kotlinx.coroutines.delay
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Everything mockup 01 (`docs/public/mockups/wearos/01-watchface.html`) needs, in
 * two shapes.
 *
 * Why this is not a registered system watch face — measured on the hardware, not
 * assumed:
 *   * Xiaomi Watch 5 (MIWEAR): the standard Wear face category
 *     (`com.google.android.wearable.action.WATCH_FACE`) is not resolved at all, and
 *     the vendor's 表盘主题选择器 lists only its own faces. Declaring the face as a
 *     WallpaperService — the shape Xiaomi's own faces have — gets it parsed by the
 *     package manager (`query-services` returns it) but it still never appears in
 *     that list.
 *   * The stock Wear emulator behaves the same: its picker lists only the two Wear
 *     sample faces, so a plain WallpaperService is not a face there either. The
 *     real mechanism is androidx.wear.watchface's WatchFaceService, which this
 *     device's vendor picker still would not enumerate.
 *
 * So mockup 01 lives where it can actually be seen: a compact clock on Home (the
 * glance) and the full face one tap away (the design). Full evidence:
 * docs/wearos-watchface-capability.md.
 */

/** Time and date, formatted by the platform so 12/24-hour and locale are honoured. */
@Composable
private fun rememberClock(): Pair<String, String> {
    val context = LocalContext.current
    val locale = Locale.getDefault()
    val timeFormat = remember(locale) { android.text.format.DateFormat.getTimeFormat(context) }
    val dateFormat = remember(locale) { SimpleDateFormat("MMM d", locale) }
    val weekdayFormat = remember(locale) { SimpleDateFormat("EEE", locale) }

    var time by remember(locale) { mutableStateOf(timeFormat.format(Date())) }
    var date by remember(locale) { mutableStateOf("") }

    LaunchedEffect(locale) {
        // Aligned to the minute: the face shows HH:mm, so writing state every
        // second would recompose this subtree 60x per visible change for no visual
        // difference — pure battery cost on a watch.
        while (true) {
            val now = Date()
            val nextTime = timeFormat.format(now)
            if (nextTime != time) time = nextTime
            val nextDate = dateFormat.format(now) + " · " + weekdayFormat.format(now)
            if (nextDate != date) date = nextDate
            val ms = System.currentTimeMillis() % 60_000L
            delay((60_000L - ms).coerceAtLeast(1_000L))
        }
    }
    return time to date
}

/**
 * Compact clock for the top of Home: time over date, nothing else.
 *
 * Deliberately not the full face: a 227dp round screen cannot hold mockup 01 plus
 * the remote-control buttons, and the buttons are the reason the app exists. The
 * full design is one tap away via [onOpenFace].
 */
@Composable
fun ClockCompact(onOpenFace: () -> Unit = {}, modifier: Modifier = Modifier) {
    val (time, date) = rememberClock()
    Column(
        modifier = modifier
            .fillMaxWidth()
            .clickable(onClick = onOpenFace),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(
            text = time,
            style = MaterialTheme.typography.title1,
            color = GodexColors.TextPrimary,
            textAlign = TextAlign.Center,
        )
        if (date.isNotEmpty()) {
            Text(
                text = date,
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
            )
        }
    }
}

/**
 * The full mockup 01 face: cardinal ticks, progress ring, four corner
 * complications and the big time.
 *
 * Every corner shows a real number, or a dash when the app has not read one yet —
 * "0 待审批" before the first refresh would be a claim, not a status.
 */
@Composable
fun WatchFace(
    running: Int = 0,
    pending: Int = 0,
    nodesOnline: Int = 0,
    nodesTotal: Int = 0,
    deviceName: String = "",
    progress: Int = 0,
    hasRunning: Boolean = false,
    read: Boolean = false,
    modifier: Modifier = Modifier,
) {
    val (time, date) = rememberClock()

    Box(
        modifier = modifier
            .fillMaxWidth()
            .aspectRatio(1f),
    ) {
        Canvas(modifier = Modifier.fillMaxSize()) {
            val stroke = size.minDimension * 0.020f
            val radius = size.minDimension / 2f - stroke * 2f
            val center = Offset(size.width / 2f, size.height / 2f)

            // Track first, so the ring reads as a ring even when idle.
            drawCircle(
                color = Color(0xFF101A1E),
                radius = radius,
                center = center,
                style = Stroke(width = stroke),
            )
            if (hasRunning) {
                // Arc length is the agent's own step percentage; at 0 steps a short
                // stub still marks "running" rather than drawing nothing (the mockup
                // uses a non-full dasharray for the same reason).
                val fraction = progress.coerceIn(0, 100) / 100f
                drawArc(
                    color = Color(0xFF14B8A6),
                    startAngle = -90f,
                    sweepAngle = if (fraction <= 0f) 60f else 360f * fraction,
                    useCenter = false,
                    topLeft = Offset(center.x - radius, center.y - radius),
                    size = Size(radius * 2, radius * 2),
                    style = Stroke(width = stroke),
                )
            }

            // Four cardinal ticks.
            val tick = Color(0xFF2B3940)
            val outer = size.minDimension / 2f - stroke
            val inner = outer - size.minDimension * 0.035f
            val tickWidth = stroke * 0.55f
            drawLine(tick, Offset(center.x, center.y - inner), Offset(center.x, center.y - outer), tickWidth)
            drawLine(tick, Offset(center.x, center.y + inner), Offset(center.x, center.y + outer), tickWidth)
            drawLine(tick, Offset(center.x - inner, center.y), Offset(center.x - outer, center.y), tickWidth)
            drawLine(tick, Offset(center.x + inner, center.y), Offset(center.x + outer, center.y), tickWidth)
        }

        // Centre: big time over the date.
        Column(
            modifier = Modifier.align(Alignment.Center),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Text(
                text = time,
                style = MaterialTheme.typography.title1,
                color = GodexColors.TextPrimary,
                textAlign = TextAlign.Center,
            )
            if (date.isNotEmpty()) {
                Text(
                    text = date,
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    textAlign = TextAlign.Center,
                )
            }
        }

        // Four corner complications, matching the mockup's slot positions.
        ComplicationSlot(
            value = if (read) running.toString() else "–",
            label = stringResource(R.string.wf_running),
            accent = if (read && running > 0) Color(0xFF38BDF8) else GodexColors.TextMuted,
            modifier = Modifier.align(Alignment.TopStart).padding(start = 18.dp, top = 30.dp),
        )
        ComplicationSlot(
            value = if (read) pending.toString() else "–",
            label = stringResource(R.string.wf_pending),
            accent = if (read && pending > 0) Color(0xFFEF4444) else GodexColors.TextMuted,
            modifier = Modifier.align(Alignment.TopEnd).padding(end = 18.dp, top = 30.dp),
        )
        ComplicationSlot(
            value = if (read) "$nodesOnline/$nodesTotal" else "–",
            label = stringResource(R.string.wf_nodes),
            accent = if (read && nodesOnline > 0) Color(0xFF22C55E) else GodexColors.TextMuted,
            modifier = Modifier.align(Alignment.BottomStart).padding(start = 18.dp, bottom = 30.dp),
        )
        ComplicationSlot(
            value = if (read) deviceName.ifBlank { "–" } else "–",
            label = stringResource(R.string.wf_device),
            accent = if (read) Color(0xFF38BDF8) else GodexColors.TextMuted,
            modifier = Modifier.align(Alignment.BottomEnd).padding(end = 18.dp, bottom = 30.dp),
        )
    }
}

/**
 * One corner complication: dot, value, label.
 *
 * The value is truncated rather than wrapped — the mockup's slot is 78px wide and
 * wrapping would run into the clock.
 */
@Composable
private fun ComplicationSlot(
    value: String,
    label: String,
    accent: Color,
    modifier: Modifier = Modifier,
) {
    Column(
        modifier = modifier,
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Canvas(modifier = Modifier.height(5.dp).fillMaxWidth(0.12f)) {
            drawCircle(color = accent, radius = size.minDimension / 2f)
        }
        Spacer(Modifier.height(1.dp))
        Text(
            text = if (value.length > 6) value.take(5) + "…" else value,
            style = MaterialTheme.typography.body2,
            fontWeight = FontWeight.Bold,
            color = accent,
            textAlign = TextAlign.Center,
            maxLines = 1,
        )
        Text(
            text = label,
            style = MaterialTheme.typography.caption1,
            color = GodexColors.TextMuted,
            textAlign = TextAlign.Center,
            maxLines = 1,
        )
    }
}

/**
 * Full-screen watch face, fed from the live session state.
 *
 * Kept as a thin adapter: it maps the screens' own state onto [WatchFace] so the
 * face cannot show numbers that disagree with the rest of the app — the running
 * count comes from the session list, approvals from the queue, and the progress
 * ring only from a turn that is actually running (the detail read keeps the last
 * finished turn's percentage, which would otherwise leave a ring on an idle face).
 */
@Composable
fun WatchFaceScreen(
    detail: com.godex.wear.data.WatchSessionDetail?,
    sessions: List<com.godex.wear.data.WatchSession>,
    approvals: List<com.godex.wear.data.WatchApproval>,
    nodes: List<com.godex.wear.data.WatchNode>,
    targetDeviceId: String,
    onBack: () -> Unit,
) {
    val device = nodes.firstOrNull { it.id == targetDeviceId }?.name.orEmpty()
    val online = nodes.count { it.status.equals("online", ignoreCase = true) }
    val running = sessions.count { it.running }
    // "read" only once there is something real to show: before the first successful
    // refresh every slot shows a dash instead of claiming zero.
    val read = nodes.isNotEmpty() || sessions.isNotEmpty() || approvals.isNotEmpty()

    RoundScreen {
        Column(
            modifier = Modifier.fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            WatchFace(
                running = running,
                pending = approvals.size,
                nodesOnline = online,
                nodesTotal = nodes.size,
                deviceName = device,
                progress = if (detail?.running == true) detail.progressPercent else 0,
                hasRunning = running > 0,
                read = read,
            )
            Spacer(Modifier.height(4.dp))
            SmallAction(
                stringResource(R.string.common_back),
                onBack,
                Modifier.fillMaxWidth(),
            )
        }
    }
}
