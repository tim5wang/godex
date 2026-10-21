package com.godex.wear.data

import android.content.Context
import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map

private val Context.dataStore: DataStore<Preferences> by preferencesDataStore(name = "godex_wear")

/**
 * Local device state.
 *
 * Holds two orthogonal pieces of state (plan §5.3):
 *
 *  - **which node** this watch is driving (`baseUrl` + credential), and
 *  - **which session** inside that node the remote control is pointed at.
 *
 * They are kept separate because switching a target device must not silently
 * change the conversation you were in, and vice versa.
 */
class WatchStore(private val context: Context) {

    private object Keys {
        val BASE_URL = stringPreferencesKey("base_url")
        val TOKEN = stringPreferencesKey("token")
        val DEVICE_ID = stringPreferencesKey("device_id")
        val DEVICE_NAME = stringPreferencesKey("device_name")
        val SCOPES = stringPreferencesKey("scopes")
        val TARGET_DEVICE = stringPreferencesKey("target_device_id")

        /**
         * Per-node session memory, serialised as `nodeId\u001FsessionId` lines.
         * A map is encoded as text because DataStore preferences only hold
         * primitives; the format is private to this file.
         */
        val SESSION_BY_DEVICE = stringPreferencesKey("session_by_device")
        val PINNED_SESSIONS = stringPreferencesKey("pinned_sessions")

        /** Set once pairing succeeds, so the app never re-pairs unnecessarily. */
        val PAIRED = booleanPreferencesKey("paired")

        // Settings screen (mockup 11). Approval alerts are deliberately absent:
        // they are mandatory, so there is nothing to store.
        val NOTIFY_ON_COMPLETE = booleanPreferencesKey("notify_on_complete")
        val POWER_SAVING = booleanPreferencesKey("power_saving")
        val AMBIENT = booleanPreferencesKey("ambient")

        /**
         * Sessions observed running at the last check, newline separated.
         *
         * Persisted rather than held in memory because the background worker runs
         * in a process that may be started fresh for it: an in-memory set would
         * always look empty on a cold start, and every session would then appear to
         * have just finished.
         */
        val RUNNING_SESSIONS = stringPreferencesKey("running_sessions")
    }

    val baseUrlFlow: Flow<String> = context.dataStore.data.map { it[Keys.BASE_URL].orEmpty() }
    val tokenFlow: Flow<String> = context.dataStore.data.map { it[Keys.TOKEN].orEmpty() }
    val deviceIdFlow: Flow<String> = context.dataStore.data.map { it[Keys.DEVICE_ID].orEmpty() }
    val deviceNameFlow: Flow<String> = context.dataStore.data.map { it[Keys.DEVICE_NAME].orEmpty() }
    val pairedFlow: Flow<Boolean> = context.dataStore.data.map { it[Keys.PAIRED] ?: false }
    val targetDeviceFlow: Flow<String> = context.dataStore.data.map { it[Keys.TARGET_DEVICE].orEmpty() }
    val scopesFlow: Flow<Set<String>> = context.dataStore.data.map { prefs ->
        prefs[Keys.SCOPES].orEmpty().split(',').map { it.trim() }.filter { it.isNotEmpty() }.toSet()
    }

    /** Everything the API client needs, read once per app start. */
    suspend fun credentials(): Credentials {
        val prefs = context.dataStore.data.first()
        return Credentials(
            baseUrl = prefs[Keys.BASE_URL].orEmpty(),
            token = prefs[Keys.TOKEN].orEmpty(),
            deviceId = prefs[Keys.DEVICE_ID].orEmpty(),
            deviceName = prefs[Keys.DEVICE_NAME].orEmpty(),
            scopes = prefs[Keys.SCOPES].orEmpty().split(',').map { it.trim() }.filter { it.isNotEmpty() }.toSet(),
            paired = prefs[Keys.PAIRED] ?: false,
        )
    }

    suspend fun saveServerUrl(url: String) {
        // Normalise here so every write path gets a usable address; a bare host
        // would otherwise hit the web UI and return SPA HTML.
        context.dataStore.edit { it[Keys.BASE_URL] = com.godex.wear.data.WatchApi.normalizeBaseUrl(url) }
    }

    /**
     * Persists the credential once pairing completes.
     *
     * The base URL is stored here as well, not only by [saveServerUrl]: a user can
     * pair straight from the default address without ever opening the settings
     * screen, and [Credentials.isUsable] requires a base URL. Omitting it left the
     * credential unusable after a restart, so the watch fell back to the pairing
     * screen even though pairing had succeeded (found by driving the app on an
     * emulator and inspecting the persisted state).
     */
    suspend fun savePairedDevice(device: WatchDevice, token: String, baseUrl: String) {
        context.dataStore.edit { prefs ->
            prefs[Keys.DEVICE_ID] = device.deviceId
            prefs[Keys.DEVICE_NAME] = device.name
            prefs[Keys.SCOPES] = device.scopes.joinToString(",")
            prefs[Keys.TOKEN] = token
            prefs[Keys.PAIRED] = true
            val normalized = com.godex.wear.data.WatchApi.normalizeBaseUrl(baseUrl)
            if (normalized.isNotEmpty()) prefs[Keys.BASE_URL] = normalized
        }
    }

    suspend fun setTargetDevice(deviceId: String) {
        context.dataStore.edit { it[Keys.TARGET_DEVICE] = deviceId }
    }

    /** Remembers the session the user last had open on a given node. */
    suspend fun rememberSession(nodeId: String, sessionId: String) {
        context.dataStore.edit { prefs ->
            val map = decodePairs(prefs[Keys.SESSION_BY_DEVICE].orEmpty())
            map[nodeId] = sessionId
            prefs[Keys.SESSION_BY_DEVICE] = encodePairs(map)
        }
    }

    suspend fun sessionForDevice(nodeId: String): String {
        val prefs = context.dataStore.data.first()
        return decodePairs(prefs[Keys.SESSION_BY_DEVICE].orEmpty())[nodeId].orEmpty()
    }

    suspend fun pinnedSessions(): List<String> {
        val prefs = context.dataStore.data.first()
        return prefs[Keys.PINNED_SESSIONS].orEmpty()
            .split('\n')
            .map { it.trim() }
            .filter { it.isNotEmpty() }
    }

    /**
     * Sessions observed running at the last check.
     *
     * Used by both the app's poller and the background worker, so a task that
     * finishes while the app is open is not announced again later in the
     * background (and vice versa).
     */
    suspend fun runningSessions(): Set<String> {
        val prefs = context.dataStore.data.first()
        return prefs[Keys.RUNNING_SESSIONS].orEmpty()
            .split('\n')
            .map { it.trim() }
            .filter { it.isNotEmpty() }
            .toSet()
    }

    suspend fun setRunningSessions(ids: Set<String>) {
        context.dataStore.edit { it[Keys.RUNNING_SESSIONS] = ids.joinToString("\n") }
    }

    /**
     * Notification / power / safety switches (mockup 11).
     *
     * Defaults are chosen for a wrist, not for a desktop: completion pings on
     * (that is the point of a watch), polling-friendliness on, and always-on
     * display off — an AMOLED wrist left glowing all day is the single fastest
     * way to empty the battery.
     */
    suspend fun preferences(): WatchPreferences {
        val prefs = context.dataStore.data.first()
        return WatchPreferences(
            notifyOnComplete = prefs[Keys.NOTIFY_ON_COMPLETE] ?: true,
            powerSaving = prefs[Keys.POWER_SAVING] ?: true,
            ambient = prefs[Keys.AMBIENT] ?: true,
        )
    }

    suspend fun setNotifyOnComplete(value: Boolean) {
        context.dataStore.edit { it[Keys.NOTIFY_ON_COMPLETE] = value }
    }

    suspend fun setPowerSaving(value: Boolean) {
        context.dataStore.edit { it[Keys.POWER_SAVING] = value }
    }

    suspend fun setAmbient(value: Boolean) {
        context.dataStore.edit { it[Keys.AMBIENT] = value }
    }

    /** Toggles a pinned session (cross-device shortcuts on the home screen). */
    suspend fun togglePinned(sessionId: String) {
        context.dataStore.edit { prefs ->
            val current = prefs[Keys.PINNED_SESSIONS].orEmpty()
                .split('\n').map { it.trim() }.filter { it.isNotEmpty() }.toMutableList()
            if (current.contains(sessionId)) current.remove(sessionId) else current.add(sessionId)
            prefs[Keys.PINNED_SESSIONS] = current.joinToString("\n")
        }
    }

    /** Clears credentials (unpair / after a 401). Never clears the server URL. */
    suspend fun clearCredentials() {
        context.dataStore.edit { prefs ->
            prefs.remove(Keys.TOKEN)
            prefs.remove(Keys.DEVICE_ID)
            prefs.remove(Keys.DEVICE_NAME)
            prefs.remove(Keys.SCOPES)
            prefs[Keys.PAIRED] = false
        }
    }

    private fun decodePairs(raw: String): MutableMap<String, String> {
        val out = mutableMapOf<String, String>()
        raw.split('\n').forEach { line ->
            if (line.isBlank()) return@forEach
            val parts = line.split(SEPARATOR)
            if (parts.size == 2) out[parts[0]] = parts[1]
        }
        return out
    }

    private fun encodePairs(map: Map<String, String>): String =
        map.entries.joinToString("\n") { it.key + SEPARATOR + it.value }

    private companion object {
        const val SEPARATOR = "\u001F" // unit separator: never occurs in ids
    }
}

/**
 * User-facing switches from the settings screen (mockup 11).
 *
 * Only switches with a real effect are modelled here. "审批请求" is not a field
 * because it cannot be turned off — an agent blocked on an approval would
 * otherwise wait forever while the watch stayed silent. "审批需二次确认" is not
 * modelled either: honouring it needs a long-press confirm path in the approval
 * screen, and shipping a switch that changes nothing would be worse than
 * omitting it.
 */
data class WatchPreferences(
    val notifyOnComplete: Boolean = true,
    /** Fewer round-trips when nothing is running; see the refresh loop. */
    val powerSaving: Boolean = true,
    /**
     * Keep a live status card on the watch face while a task runs (mockup 12).
     *
     * This is the honest app-level mapping of "常亮显示": a true always-on face is
     * a watch-face capability, but the ongoing-activity card is the ambient
     * surface an app can actually control.
     */
    val ambient: Boolean = true,
)

/** Snapshot of everything needed to make an authenticated call. */
data class Credentials(
    val baseUrl: String,
    val token: String,
    val deviceId: String,
    val deviceName: String,
    val scopes: Set<String>,
    val paired: Boolean,
) {
    val isUsable: Boolean get() = paired && token.isNotEmpty() && baseUrl.isNotEmpty()
    fun canCommand(): Boolean = scopes.contains(WatchDevice.SCOPE_COMMAND)
    fun canApprove(): Boolean = scopes.contains(WatchDevice.SCOPE_APPROVE)
    fun canRead(): Boolean = scopes.contains(WatchDevice.SCOPE_READ)
}
