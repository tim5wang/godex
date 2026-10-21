package com.godex.wear

import android.app.Application
import com.godex.wear.notify.WearNotifier

/**
 * Application entry point.
 *
 * State lives in [com.godex.wear.data] and DataStore rather than in global
 * mutable state, so the only work here is creating the notification channels.
 * They are created once at process start instead of before each post: creating an
 * existing channel is a no-op, but it would still cross a binder boundary on
 * every poll, and binder calls are not free on a watch.
 */
class GodexWearApp : Application() {
    override fun onCreate() {
        super.onCreate()
        WearNotifier.ensureChannels(this)
    }
}
