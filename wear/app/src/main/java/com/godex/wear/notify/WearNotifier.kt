package com.godex.wear.notify

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.wear.ongoing.OngoingActivity
import androidx.wear.ongoing.Status
import com.godex.wear.MainActivity
import com.godex.wear.R
import com.godex.wear.data.WatchApproval
import com.godex.wear.data.WatchSessionDetail
import java.time.Instant
import java.time.OffsetDateTime

/**
 * Watch-surface notifications and the ongoing activity card.
 *
 * Implements mockups 08 (notification with executable actions) and 12 (ongoing
 * activity / live update).
 *
 * Everything here uses plain AndroidX + platform APIs. That is a hard
 * requirement, not a preference: the target device is a China-market watch with
 * no Google Play services, so FCM and any Play-services-dependent helper are
 * simply unavailable. `NotificationCompat` and `OngoingActivity` are the
 * no-GMS equivalents, and the OS bridges the notification to the paired phone
 * and back without an app-level push channel.
 */
object WearNotifier {

    /** Approval requests; high priority because the agent is blocked on them. */
    const val CHANNEL_APPROVALS = "godex_approvals"

    /** The ongoing "a task is running" card. Silent: it is ambient, not an alert. */
    const val CHANNEL_TASK = "godex_task"

    /**
     * Turn finished / failed.
     *
     * Separate from [CHANNEL_TASK] because that one is IMPORTANCE_LOW, and a
     * completion posted there never vibrated or made a sound — so "the reply came
     * back" was silent unless the user happened to be looking at the screen. A
     * completion is the one event the watch exists to deliver, so it gets its own
     * alerting channel.
     */
    const val CHANNEL_COMPLETE = "godex_complete"

    private const val ONGOING_NOTIFICATION_ID = 41
    private const val APPROVAL_NOTIFICATION_ID = 42
    private const val COMPLETE_NOTIFICATION_ID = 43

    /**
     * Creates the channels once.
     *
     * Called from the application object rather than per-post: creating an
     * existing channel is a no-op, but doing it on every poll would still cross
     * a binder boundary on a device where binder calls are not free.
     */
    fun ensureChannels(context: Context) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return

        // IMPORTANCE_HIGH so an approval interrupts: the agent is blocked until it
        // is answered, and a quiet notification would be read hours later.
        val approvals = NotificationChannel(
            CHANNEL_APPROVALS,
            "审批请求",
            NotificationManager.IMPORTANCE_HIGH,
        ).apply {
            description = "智能体等待批准的操作"
            enableVibration(true)
        }

        // IMPORTANCE_LOW: the running card is status, not an alert. Buzzing on
        // every poll would make the watch unusable.
        val task = NotificationChannel(
            CHANNEL_TASK,
            "任务运行中",
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = "当前任务的常驻状态"
            setShowBadge(false)
        }

        // IMPORTANCE_DEFAULT + vibration: a finished turn is what the user is
        // waiting for, so it must reach the wrist even with the app in the
        // background. Not HIGH — it is informational, and the approval channel
        // stays the only interrupting one.
        val complete = NotificationChannel(
            CHANNEL_COMPLETE,
            "任务完成",
            NotificationManager.IMPORTANCE_DEFAULT,
        ).apply {
            description = "对话返回、任务完成或失败"
            enableVibration(true)
            setShowBadge(true)
        }

        manager.createNotificationChannel(approvals)
        manager.createNotificationChannel(task)
        manager.createNotificationChannel(complete)
    }

    // ---- mockup 08: approval notification with executable actions ----

    /**
     * Posts (or replaces) the approval notification for the head of the queue.
     *
     * Only the newest request is shown: a wrist notification shade can hold a
     * couple of entries, and the in-app queue (mockup 15) is the complete view.
     * Showing five indistinguishable "approve?" alerts would be noise.
     */
    fun notifyApproval(context: Context, approval: WatchApproval, sessionTitle: String) {
        val sessionId = approval.sessionId ?: return
        val requestId = approval.requestId ?: return

        val notification = NotificationCompat.Builder(context, CHANNEL_APPROVALS)
            .setSmallIcon(R.drawable.ic_godex)
            .setContentTitle("需要批准")
            .setContentText(approval.summary.ifBlank { approval.toolName ?: "工具调用" })
            .setStyle(
                NotificationCompat.BigTextStyle().bigText(
                    buildString {
                        append(approval.summary.ifBlank { approval.toolName ?: "工具调用" })
                        if (sessionTitle.isNotBlank()) append("\n会话：").append(sessionTitle)
                    },
                ),
            )
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            // CATEGORY_REMINDER: the closest platform category to "the agent is
            // blocked until you answer". CATEGORY_REMOTE_INPUT does not exist in
            // androidx.core (verified against the compiled artifact).
            .setCategory(NotificationCompat.CATEGORY_REMINDER)
            // Tapping opens the app straight on the approval screen (deep link).
            // The key lets the app find this exact request in the queue after a
            // refresh, and lets it say "already handled" when it is gone.
            .setContentIntent(openIntent(context, sessionId, approval.key))
            .addAction(action(context, R.drawable.ic_godex, "批准", WatchActionReceiver.ACTION_APPROVE, sessionId, requestId, 1))
            .addAction(action(context, R.drawable.ic_godex, "拒绝", WatchActionReceiver.ACTION_DENY, sessionId, requestId, 2))
            .setAutoCancel(true)
            .setOnlyAlertOnce(false)
            .build()

        // POST_NOTIFICATIONS may be denied; posting is best-effort by design, and
        // the in-app queue still shows the request.
        runCatching {
            NotificationManagerCompat.from(context).notify(APPROVAL_NOTIFICATION_ID, notification)
        }
    }

    /** Removes the approval notification once the queue is empty or handled. */
    fun clearApproval(context: Context) {
        runCatching { NotificationManagerCompat.from(context).cancel(APPROVAL_NOTIFICATION_ID) }
    }

    // ---- mockup 08: "turn finished" notification ----

    /**
     * Tells the user a turn they were waiting on has finished.
     *
     * Distinct id from the approval notification so a completion never silently
     * replaces a pending approval — the approval is the actionable one.
     *
     * Posted on [CHANNEL_COMPLETE], not [CHANNEL_TASK]: the latter is
     * IMPORTANCE_LOW, so a completion there was delivered silently and the watch
     * never buzzed — which is precisely the "app in the background, reply came
     * back, nothing happened" complaint.
     *
     * A failed turn is announced as a failure rather than folded into "完成": the
     * user needs to know to look, and on a wrist the title is often all that is read.
     */
    fun notifyTurnFinished(
        context: Context,
        sessionTitle: String,
        sessionId: String,
        failed: Boolean = false,
    ) {
        val title = sessionTitle.ifBlank { "会话" }
        val notification = NotificationCompat.Builder(context, CHANNEL_COMPLETE)
            .setSmallIcon(R.drawable.ic_godex)
            .setContentTitle(if (failed) "任务失败" else "任务完成")
            .setContentText("$title · 点按看详情")
            .setPriority(
                if (failed) NotificationCompat.PRIORITY_HIGH else NotificationCompat.PRIORITY_DEFAULT,
            )
            // CATEGORY_STATUS: informational, not a call to action that must
            // interrupt. The approval channel remains the interrupting one.
            .setCategory(NotificationCompat.CATEGORY_STATUS)
            .setContentIntent(openIntent(context, sessionId))
            .setAutoCancel(true)
            .build()

        runCatching {
            NotificationManagerCompat.from(context).notify(COMPLETE_NOTIFICATION_ID, notification)
        }
    }

    // ---- mockup 12: ongoing activity (live card on the watch face) ----

    /**
     * Drives the ongoing-activity card from the current session detail.
     *
     * When nothing is running the card is removed, which is the point of an
     * ongoing activity: it exists only while there is something to be ongoing
     * about. A permanent "idle" card would be a notification the user cannot
     * dismiss.
     */
    fun updateOngoing(context: Context, detail: WatchSessionDetail?, sessionTitle: String) {
        val manager = NotificationManagerCompat.from(context)
        if (detail == null || !detail.running) {
            runCatching { manager.cancel(ONGOING_NOTIFICATION_ID) }
            return
        }

        val title = sessionTitle.ifBlank { "GoDex" }
        // OngoingActivity.Builder takes the *builder*, not the built
        // notification: it adds its own extras and posts it itself via apply().
        val notification = NotificationCompat.Builder(context, CHANNEL_TASK)
            .setSmallIcon(R.drawable.ic_godex)
            .setContentTitle(title)
            .setContentText(progressText(detail))
            .setContentIntent(openIntent(context, detail.sessionId))
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)

        // build() validates its own inputs and throws IllegalArgumentException when a
        // static icon or touch intent is missing, and openIntent ultimately calls
        // PendingIntent.getActivity, which can fail on an overloaded intent. This
        // runs inside a ViewModel coroutine whose catch handles only
        // WatchApiException, so an escaping throw here would take the screen down
        // over a cosmetic live card. The card is advisory: failing to post it must
        // never be fatal.
        runCatching {
            OngoingActivity.Builder(context, ONGOING_NOTIFICATION_ID, notification)
                .setStaticIcon(R.drawable.ic_godex)
                .setTitle(title)
                .setTouchIntent(openIntent(context, detail.sessionId))
                .setContentDescription(progressText(detail))
                .setStatus(statusFor(detail))
                .build()
                .apply(context)
        }
    }

    /**
     * Builds the live status line.
     *
     * A stopwatch part is used when the server sent a start timestamp: the watch
     * face then ticks the elapsed time itself, so the number stays correct with
     * no further network traffic. Without a timestamp it degrades to static text
     * rather than showing a timer started at an arbitrary moment.
     */
    private fun statusFor(detail: WatchSessionDetail): Status {
        val text = progressText(detail)
        val started = parseInstant(detail.startedAt)
        return if (started != null) {
            Status.Builder()
                .addTemplate("#progress# · 已用时 #elapsed#")
                .addPart("progress", Status.TextPart(text))
                .addPart("elapsed", Status.StopwatchPart(started.toEpochMilli()))
                .build()
        } else {
            Status.Builder()
                .addTemplate("#progress#")
                .addPart("progress", Status.TextPart(text))
                .build()
        }
    }

    /** One-line progress summary, preferring the agent's own step count. */
    private fun progressText(detail: WatchSessionDetail): String = when {
        detail.hasSteps -> "第 ${detail.stepDone}/${detail.stepTotal} 步 · ${detail.progressPercent}%"
        !detail.currentTool.isNullOrBlank() -> "正在使用 ${detail.currentTool}"
        else -> "正在执行"
    }

    private fun parseInstant(raw: String?): Instant? {
        if (raw.isNullOrBlank()) return null
        return runCatching { Instant.parse(raw) }.getOrElse {
            runCatching { OffsetDateTime.parse(raw).toInstant() }.getOrNull()
        }
    }

    // ---- intents ----

    /**
     * Deep link into the app.
     *
     * The notification action buttons must not depend on an app-visible service,
     * so they are broadcast intents (see [WatchActionReceiver]); this content
     * intent, by contrast, simply opens the UI.
     *
     * [approvalKey] is set for approval alerts so the tap can land on the approval
     * screen itself (mockup 08). Carrying only a session id used to send every
     * alert — including approval alerts — to the session detail screen, which is
     * not where the pending decision lives.
     */
    private fun openIntent(
        context: Context,
        sessionId: String,
        approvalKey: String? = null,
    ): PendingIntent {
        val intent = Intent(context, MainActivity::class.java).apply {
            action = Intent.ACTION_VIEW
            putExtra(EXTRA_SESSION_ID, sessionId)
            if (!approvalKey.isNullOrEmpty()) putExtra(EXTRA_APPROVAL_KEY, approvalKey)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
        }
        // Distinct request codes per target: a shared code would make two pending
        // intents collide, so the approval link could overwrite the session link.
        val requestCode = (sessionId + (approvalKey ?: "")).hashCode()
        return PendingIntent.getActivity(
            context,
            requestCode,
            intent,
            // FLAG_IMMUTABLE is mandatory from API 31.
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
    }

    private fun action(
        context: Context,
        icon: Int,
        label: String,
        action: String,
        sessionId: String,
        requestId: String,
        requestCode: Int,
    ): NotificationCompat.Action {
        val intent = Intent(context, WatchActionReceiver::class.java).apply {
            this.action = action
            putExtra(WatchActionReceiver.EXTRA_SESSION_ID, sessionId)
            putExtra(WatchActionReceiver.EXTRA_REQUEST_ID, requestId)
        }
        val pending = PendingIntent.getBroadcast(
            context,
            requestCode,
            intent,
            // FLAG_IMMUTABLE: the receiver reads fixed extras, so nothing needs to
            // be able to rewrite this intent.
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Action.Builder(icon, label, pending).build()
    }

    /** Session id carried by a deep link, if any. */
    const val EXTRA_SESSION_ID = "godex_session_id"

    /** Approval key carried by an approval deep link, if any (mockup 06/08). */
    const val EXTRA_APPROVAL_KEY = "godex_approval_key"
}
