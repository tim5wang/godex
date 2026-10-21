package com.godex.wear.tile

import androidx.wear.tiles.ActionBuilders
import androidx.wear.tiles.DimensionBuilders
import androidx.wear.tiles.LayoutElementBuilders
import androidx.wear.tiles.ModifiersBuilders
import androidx.wear.tiles.RequestBuilders
import androidx.wear.tiles.TileBuilders
import androidx.wear.tiles.TileService
import androidx.wear.tiles.TimelineBuilders
import androidx.concurrent.futures.ResolvableFuture
import com.godex.wear.data.Credentials
import com.godex.wear.data.WatchApi
import com.godex.wear.data.WatchStore
import com.google.common.util.concurrent.ListenableFuture
import kotlinx.coroutines.runBlocking

/**
 * Tile: "a glance is enough" (mockup 02).
 *
 * The Tile answers exactly one question — is anything waiting for me? — and it
 * is populated from the same `/watch/overview` payload the app's home screen
 * uses, so a Tile and the app can never disagree about the state.
 *
 * Clean build target note: a Tile is rendered by the system shell, not by our
 * process, so it must be produced synchronously inside `onTileRequest`. The
 * network read is therefore done with a bounded blocking call rather than a
 * coroutine: the API exposes a `ListenableFuture`, and the system gives the
 * service a short window to answer. Bounding it to a few seconds means a dead
 * node degrades to an explanatory Tile instead of a blank one.
 */
class GodexTileService : TileService() {

    /**
     * Freshness window.
     *
     * The shell will not ask again before this elapses, so it doubles as a
     * battery/promptness dial. 15 minutes matches how fast the underlying state
     * actually changes (an agent finishing a task), rather than re-reading on
     * every wrist raise.
     */
    private val freshnessMillis = 15 * 60 * 1000L

    override fun onTileRequest(
        requestParams: RequestBuilders.TileRequest,
    ): ListenableFuture<TileBuilders.Tile> {
        val summary = loadSummary()
        val tile = TileBuilders.Tile.Builder()
            .setResourcesVersion(RESOURCES_VERSION)
            .setFreshnessIntervalMillis(freshnessMillis)
            // setTimeline (not setTileTimeline) is the overload that takes the
            // androidx.wear.tiles Timeline this builder produces; the other one
            // expects the protolayout Timeline type.
            .setTimeline(TimelineBuilders.Timeline.fromLayoutElement(buildLayout(summary)))
            .build()
        return futureOf(tile)
    }

    /**
     * Completes a ListenableFuture synchronously.
     *
     * `ResolvableFuture` is used rather than guava's `Futures.immediateFuture`
     * because the tiles dependency pulls in only the empty `listenablefuture`
     * artifact, not guava itself — `Futures` would not compile here.
     */
    private fun <T> futureOf(value: T): ListenableFuture<T> =
        ResolvableFuture.create<T>().apply { set(value) }

    /**
     * One bounded read of the aggregate endpoint.
     *
     * Failures are folded into a [TileSummary] rather than thrown: the system
     * shell renders whatever we return, and an exception would surface as an
     * empty tile with no explanation. "读不到" tells the user the node is
     * unreachable, which is actionable.
     */
    private fun loadSummary(): TileSummary {
        val credentials: Credentials = runBlocking { WatchStore(applicationContext).credentials() }
        if (!credentials.isUsable) return TileSummary.Unpaired

        val api = WatchApi().apply {
            baseUrl = credentials.baseUrl.ifEmpty { WatchApi.DEFAULT_EMULATOR_URL }
            token = credentials.token
        }
        return try {
            val overview = runBlocking { api.overview() }
            val sessions = runBlocking { api.sessions() }
            val current = sessions.firstOrNull()
            TileSummary.Ready(
                pending = overview.pendingApprovals,
                active = overview.activeSessions,
                sessionTitle = current?.title?.takeIf { it.isNotBlank() } ?: "未命名会话",
                hasSession = current != null,
                running = current?.running == true,
            )
        } catch (err: Exception) {
            TileSummary.Unreachable
        }
    }

    private fun buildLayout(summary: TileSummary): LayoutElementBuilders.LayoutElement {
        val column = LayoutElementBuilders.Column.Builder()

        val headline: String
        val detail: String
        when (summary) {
            is TileSummary.Unpaired -> {
                headline = "未配对"
                detail = "打开应用完成配对"
            }
            is TileSummary.Unreachable -> {
                headline = "读不到"
                detail = "电脑端可能未启动"
            }
            is TileSummary.Ready -> {
                headline = when {
                    summary.pending > 0 -> "${summary.pending} 项待批准"
                    summary.running -> summary.sessionTitle
                    summary.hasSession -> summary.sessionTitle
                    else -> "没有会话"
                }
                detail = when {
                    summary.pending > 0 -> "点按查看"
                    summary.running -> "运行中"
                    summary.active > 0 -> "${summary.active} 个运行中"
                    else -> "点按打开"
                }
            }
        }

        column.addContent(
            LayoutElementBuilders.Text.Builder()
                .setText(
                    androidx.wear.tiles.TypeBuilders.StringProp.Builder().setValue(headline).build(),
                )
                .setFontStyle(
                    LayoutElementBuilders.FontStyle.Builder()
                        .setWeight(LayoutElementBuilders.FONT_WEIGHT_BOLD)
                        .build(),
                )
                // The whole tile is one tap target; a Tile is too small for
                // per-element actions to be reliably hittable.
                .setModifiers(
                    ModifiersBuilders.Modifiers.Builder()
                        .setClickable(
                            ModifiersBuilders.Clickable.Builder()
                                .setOnClick(openAppAction())
                                .build(),
                        )
                        .build(),
                )
                .build(),
        )

        column.addContent(
            LayoutElementBuilders.Text.Builder()
                .setText(androidx.wear.tiles.TypeBuilders.StringProp.Builder().setValue(detail).build())
                .setMaxLines(2)
                .build(),
        )

        return LayoutElementBuilders.Box.Builder()
            .setWidth(DimensionBuilders.expand())
            .setHeight(DimensionBuilders.expand())
            .setHorizontalAlignment(LayoutElementBuilders.HORIZONTAL_ALIGN_CENTER)
            .setVerticalAlignment(LayoutElementBuilders.VERTICAL_ALIGN_CENTER)
            .addContent(column.build())
            .build()
    }

    /** Launches the app's main activity; no extras are needed for a glance. */
    private fun openAppAction(): ActionBuilders.Action =
        ActionBuilders.LaunchAction.Builder()
            .setAndroidActivity(
                ActionBuilders.AndroidActivity.Builder()
                    .setPackageName(packageName)
                    .setClassName("com.godex.wear.MainActivity")
                    .build(),
            )
            .build()

    private sealed interface TileSummary {
        data object Unpaired : TileSummary
        data object Unreachable : TileSummary
        data class Ready(
            val pending: Int,
            val active: Int,
            val sessionTitle: String,
            val hasSession: Boolean,
            val running: Boolean,
        ) : TileSummary
    }

    private companion object {
        /**
         * Bumped only when the Tile's layout changes; the shell caches by version.
         * Ship resources are static, so a constant is correct rather than lazy.
         */
        const val RESOURCES_VERSION = "1"
    }
}
