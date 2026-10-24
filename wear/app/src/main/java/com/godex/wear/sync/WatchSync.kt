package com.godex.wear.sync

import android.content.Context
import android.util.Log
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.godex.wear.data.WatchApi
import com.godex.wear.data.WatchApiException
import com.godex.wear.data.WatchApproval
import com.godex.wear.data.WatchSession
import com.godex.wear.data.WatchStore
import com.godex.wear.notify.WearNotifier
import java.util.concurrent.TimeUnit

/**
 * Background pull, so a notification can arrive while the app is closed.
 *
 * Before this the only refresh loop was tied to the UI lifecycle
 * ([com.godex.wear.ui.WatchViewModel.startPolling]): with the app not in the
 * foreground nothing polled, so "the agent needs approval" could not reach the
 * wrist at all. That made the notification surface (mockup 08) work only while
 * the user was already looking at the app — which is exactly when it is least
 * needed.
 *
 * **Why WorkManager.** It is pure AndroidX with no Google Play services, so it
 * runs on the China-market target where GMS is absent. It also inherits the
 * system's doze/reboot handling, which a hand-rolled AlarmManager loop would have
 * to reimplement (and typically gets wrong). The cost is one dependency; the
 * alternative — and the reason this was deferred — is that scheduling is the part
 * of a background feature that is easy to fake and hard to get right.
 *
 * Interval is 15 minutes because that is WorkManager's floor for periodic work.
 * That is the honest trade-off for a watch: faster would defeat the battery goal,
 * and anything missed is still visible the moment the app is opened.
 */
object WatchSync {

    private const val WORK_NAME = "godex-watch-sync"

    /**
     * Whether the app is currently on screen.
     *
     * The in-app poller already posts the same notifications while the user is
     * looking at the app, and it polls more often (10–90s versus 15min). Letting
     * both run would mean two alerts for one event — on a wrist that is a double
     * buzz for something the user is already reading. WorkManager runs in this
     * process, so a plain flag is enough; no IPC is involved.
     */
    @Volatile
    var appVisible: Boolean = false

    /**
     * Ensures the periodic pull is running.
     *
     * [ExistingPeriodicWorkPolicy.KEEP] makes this idempotent, so it can be called
     * on every start and after every pairing without resetting the interval (an
     * UPDATE policy would postpone the next run indefinitely if the app happened to
     * be opened often).
     */
    fun schedule(context: Context) {
        val request = PeriodicWorkRequestBuilder<WatchSyncWorker>(15, TimeUnit.MINUTES)
            .setConstraints(
                // Do not wake the radio when there is no connectivity: the pull can
                // only produce a notification, and it will run when a network
                // appears.
                Constraints.Builder().setRequiredNetworkType(NetworkType.CONNECTED).build(),
            )
            .build()

        WorkManager.getInstance(context).enqueueUniquePeriodicWork(
            WORK_NAME,
            ExistingPeriodicWorkPolicy.KEEP,
            request,
        )
    }

    /** Stops the periodic pull, e.g. after unpairing. */
    fun cancel(context: Context) {
        WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME)
    }
}

/**
 * One periodic read of the aggregate endpoints, posting whatever needs the user.
 *
 * It deliberately reuses the same notification call sites as the foreground
 * poller rather than building its own alerts, so a background notification and an
 * in-app one can never disagree about the state they describe.
 */
class WatchSyncWorker(
    context: Context,
    params: WorkerParameters,
) : CoroutineWorker(context, params) {

    override suspend fun doWork(): Result {
        // The visible case is the in-app poller's job (it polls far more often).
        if (WatchSync.appVisible) {
            Log.i(TAG, "skip: app is visible (foreground poller owns notifications)")
            return Result.success()
        }

        val store = WatchStore(applicationContext)
        val credentials = store.credentials()
        if (!credentials.isUsable) {
            // Not paired: there is nothing to pull, and this is not a failure.
            Log.i(TAG, "skip: credentials unusable (paired=${credentials.paired} token=${credentials.token.isNotEmpty()} url=${credentials.baseUrl.isNotEmpty()})")
            return Result.success()
        }

        val api = WatchApi().apply {
            baseUrl = credentials.baseUrl.ifEmpty { WatchApi.DEFAULT_EMULATOR_URL }
            token = credentials.token
        }

        return try {
            val sessions = api.sessions()
            val approvals = api.approvals()
            Log.i(TAG, "run: base=${api.baseUrl} sessions=${sessions.size} approvals=${approvals.size}")
            announceApproval(approvals, sessions)
            announceFinishedSessions(
                context = applicationContext,
                sessions = sessions,
                store = store,
                notifyEnabled = store.preferences().notifyOnComplete,
            )
            Result.success()
        } catch (err: WatchApiException) {
            Log.w(TAG, "api error: status=${err.statusCode} msg=${err.message} auth=${err.isAuthFailure}")
            // Credential revoked or expired: mirror the app's behaviour so a stale
            // token does not keep waking the radio forever. The app also clears
            // this, but it may not be opened for days.
            if (err.isAuthFailure) {
                store.clearCredentials()
                WatchSync.cancel(applicationContext)
                Result.success()
            } else {
                Result.retry()
            }
        } catch (err: Exception) {
            // A dead node is expected (the user's computer is simply off), so this
            // is a retry rather than an error: the next run should succeed without
            // any user action.
            Log.w(TAG, "retry after ${err.javaClass.simpleName}: ${err.message}")
            Result.retry()
        }
    }

    /**
     * Mirrors the approval queue onto the notification surface.
     *
     * Same rule as the foreground path: only the first actionable item is posted
     * (a watch shade is tiny, and the in-app queue is the complete view), and an
     * empty queue clears the alert — a stale "approve?" prompt offering buttons for
     * an already-answered request is actively harmful.
     *
     * This is level-triggered and uses a fixed notification id, so re-posting the
     * same request simply updates it instead of stacking duplicates.
     */
    private fun announceApproval(approvals: List<WatchApproval>, sessions: List<WatchSession>) {
        val head = approvals.firstOrNull { it.isPermission }
        if (head == null) {
            Log.i(TAG, "clear approval notification: queue has no permission request")
            WearNotifier.clearApproval(applicationContext)
            return
        }
        Log.i(TAG, "post approval notification: ${head.key}")
        val sessionId = head.sessionId.orEmpty()
        val title = sessions.firstOrNull { it.sessionId == sessionId }?.title.orEmpty()
        WearNotifier.notifyApproval(applicationContext, head, title)
    }

    private companion object {
        const val TAG = "GodexWatchSync"
    }
}

/**
 * Announces turns that finished since the last check.
 *
 * Shared by the foreground poller and the background worker on purpose. Both
 * observe the same transitions from opposite sides of the app's visibility, so
 * with separate bookkeeping a task finishing while the app was open would be
 * announced by the in-app poller and then announced again by the next background
 * run — on a wrist, a second buzz for something already read. The bookkeeping is
 * persisted rather than kept in memory because the worker's process may be started
 * fresh, and empty in-memory state would make every idle session look new.
 *
 * **Turn-level, not running-level.** The previous rule was "was running, now is
 * not". That cannot see a turn that starts and ends between two checks: the node
 * reports running=false both times, so the transition never appears. On a watch the
 * background cadence is tens of minutes, so that silently excluded most turns —
 * precisely the case the user asked about (a reply arrives while the app is in the
 * background and nothing alerts). The node now sends the newest turn id and its
 * status, so a turn is announced when it reaches a terminal status and its id has
 * not been announced before. This also fixes the opposite error: a turn that is
 * already finished the first time the watch ever sees a session is not announced,
 * because there is no evidence the user was waiting for it.
 *
 * A session that dropped out of the list is deliberately NOT announced: the list is
 * paginated, so absence proves nothing about whether the turn finished or merely
 * aged out. Announcing from absence would be inventing a fact.
 */
suspend fun announceFinishedSessions(
    context: Context,
    sessions: List<WatchSession>,
    store: WatchStore,
    notifyEnabled: Boolean,
) {
    val previouslyRunning = store.runningSessions()
    val announced = store.announcedTurns().toMutableMap()

    // Sessions whose turn we have never seen are only recorded, never announced:
    // a session appearing in the list for the first time may well have finished
    // hours ago, and buzzing for that would be inventing a fact. A session counts
    // as "known" if it was running at the last check or if we already recorded a
    // turn for it.
    val known = previouslyRunning + announced.keys
    var announcedChanged = false

    sessions.forEach { session ->
        val turnId = session.lastTurnId?.takeIf { it.isNotBlank() } ?: return@forEach
        val previousTurn = announced[session.sessionId]

        if (session.lastTurnFinished && turnId != previousTurn) {
            // Only alert for a turn we watched in flight — i.e. the session was
            // running at the last check, or we had already seen one of its turns.
            val watched = session.sessionId in known
            if (notifyEnabled && watched) {
                val failed = session.lastTurnStatus == "failed"
                WearNotifier.notifyTurnFinished(
                    context = context,
                    sessionTitle = session.title,
                    sessionId = session.sessionId,
                    failed = failed,
                )
            }
            announced[session.sessionId] = turnId
            announcedChanged = true
        } else if (session.running && turnId != previousTurn) {
            // Remember the turn as soon as it is in flight, so that a turn which
            // finishes before the next check is still recognised as one we saw
            // start. This is what makes short turns announceable at all.
            announced[session.sessionId] = turnId
            announcedChanged = true
        }
    }

    // Recorded whether or not the user wants completion alerts: this is state, not
    // a notification preference, and skipping it would make the next check treat
    // an already-announced turn as new. Written only when it actually changed,
    // because the foreground poller calls this every 10–90s and an unconditional
    // DataStore write per poll is needless flash I/O on a watch.
    if (announcedChanged) store.setAnnouncedTurns(announced)

    val runningNow = sessions.filter { it.running }.map { it.sessionId }.toSet()
    if (runningNow != previouslyRunning) store.setRunningSessions(runningNow)
}
