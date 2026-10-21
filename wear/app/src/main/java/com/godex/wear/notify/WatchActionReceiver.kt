package com.godex.wear.notify

import android.app.NotificationManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.godex.wear.data.Credentials
import com.godex.wear.data.WatchApi
import com.godex.wear.data.WatchApiException
import com.godex.wear.data.WatchStore
import kotlinx.coroutines.runBlocking

/**
 * Handles the approve/deny buttons on the approval notification (mockup 08).
 *
 * A broadcast receiver rather than an activity: the notification action must work
 * without the UI being launched, and on a watch waking the whole app to answer a
 * single prompt wastes battery. `goAsync` keeps the process alive just long
 * enough for the network call.
 *
 * This is the no-GMS path the plan requires: `NotificationCompat.Action` is a
 * platform capability, so the buttons survive on a device where FCM cannot be
 * used at all.
 */
class WatchActionReceiver : BroadcastReceiver() {

    override fun onReceive(context: Context, intent: Intent) {
        val action = intent.action ?: return
        if (action != ACTION_APPROVE && action != ACTION_DENY) return

        val sessionId = intent.getStringExtra(EXTRA_SESSION_ID).orEmpty()
        val requestId = intent.getStringExtra(EXTRA_REQUEST_ID).orEmpty()
        if (sessionId.isEmpty() || requestId.isEmpty()) return

        val pending = goAsync()
        val appContext = context.applicationContext

        // A short blocking call in a receiver is acceptable here because
        // goAsync() explicitly extends the process lifetime for it, and the
        // alternative (a WorkManager job) would add a dependency for one request.
        Thread {
            try {
                val store = WatchStore(appContext)
                val credentials: Credentials = runBlocking { store.credentials() }
                val api = WatchApi().apply {
                    baseUrl = credentials.baseUrl.ifEmpty { WatchApi.DEFAULT_EMULATOR_URL }
                    token = credentials.token
                }
                runBlocking {
                    if (action == ACTION_APPROVE) {
                        // Narrowest grant, matching the in-app behaviour: a
                        // notification tap must never widen authority silently.
                        resolve(api, sessionId, requestId, approve = true)
                    } else {
                        resolve(api, sessionId, requestId, approve = false)
                    }
                }
                // Resolved: the queue entry is gone, so the notification must go
                // too. Leaving it would offer buttons for an already-answered
                // request.
                runCatching {
                    context.getSystemService(NotificationManager::class.java)?.cancel(APPROVAL_ID)
                }
            } catch (err: Exception) {
                // The push queue is a convenience surface: a failure here is
                // recoverable in the app (mockup 06/15), so it is deliberately
                // swallowed rather than surfaced as a crash on the wrist.
            } finally {
                pending.finish()
            }
        }.start()
    }

    private suspend fun resolve(api: WatchApi, sessionId: String, requestId: String, approve: Boolean) {
        try {
            if (approve) {
                api.approve(sessionId, requestId, "once")
            } else {
                api.deny(sessionId, requestId)
            }
        } catch (err: WatchApiException) {
            // 409 = another client already resolved it. That is a normal race on a
            // shared queue, not a failure worth retrying.
            if (err.statusCode != 409) throw err
        }
    }

    companion object {
        const val ACTION_APPROVE = "com.godex.wear.action.APPROVE"
        const val ACTION_DENY = "com.godex.wear.action.DENY"
        const val EXTRA_SESSION_ID = "session_id"
        const val EXTRA_REQUEST_ID = "request_id"

        /** Matches WearNotifier's approval notification id. */
        private const val APPROVAL_ID = 42
    }
}
