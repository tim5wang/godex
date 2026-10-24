package com.godex.wear.ui

import androidx.compose.foundation.layout.Arrangement
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
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import com.godex.wear.R
import com.godex.wear.data.PairingStart
import com.godex.wear.data.WatchApi
import com.godex.wear.data.WatchApiException
import com.godex.wear.data.WatchDevice
import kotlinx.coroutines.delay

/**
 * Device-authorization pairing (plan §4 W2).
 *
 * The watch shows a short code; the user confirms it on an already-trusted
 * endpoint. The watch never receives the node's web token, only its own
 * scope-restricted credential — so a lost watch cannot be used to reconfigure
 * the node.
 */
@Composable
fun PairingScreen(
    api: WatchApi,
    serverUrl: String,
    onServerUrlChange: (String) -> Unit,
    onPaired: (WatchDevice, String) -> Unit,
    onSettings: () -> Unit,
) {
    var pairing by remember { mutableStateOf<PairingStart?>(null) }
    var status by remember { mutableStateOf("") }
    var error by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }

    // Needed because the polling and startup effects below are not composable scopes:
    // status/error text is built there, and stringResource is only callable from
    // composition.
    val context = LocalContext.current

    // Poll loop: created only once a pairing exists, and cancelled automatically
    // when the composable leaves or `pairing` changes, so a watch that is put
    // down stops polling instead of draining the battery.
    LaunchedEffect(pairing?.deviceCode) {
        val current = pairing ?: return@LaunchedEffect
        repeat(PAIR_POLL_ATTEMPTS) {
            delay(PAIR_POLL_INTERVAL_MS)
            try {
                val result = api.exchangeToken(current.deviceCode)
                when {
                    result.isApproved -> {
                        val device = result.device
                        val token = result.token
                        if (device != null && !token.isNullOrEmpty()) {
                            onPaired(device, token)
                        } else {
                            error = context.getString(R.string.pairing_incomplete)
                        }
                        return@LaunchedEffect
                    }
                    result.isExpired -> {
                        error = context.getString(R.string.pairing_expired)
                        pairing = null
                        status = ""
                        return@LaunchedEffect
                    }
                    else -> { /* still pending: keep waiting */ }
                }
            } catch (err: WatchApiException) {
                error = err.message
                return@LaunchedEffect
            } catch (err: Exception) {
                error = err.message ?: context.getString(R.string.pairing_network_unreachable)
                return@LaunchedEffect
            }
        }
        error = context.getString(R.string.pairing_timeout)
    }

    RoundScreen {
        Column(
            modifier = Modifier.fillMaxWidth(),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.Center,
        ) {
            val shown = pairing
            if (shown == null) {
                ScreenTitle(
                    stringResource(R.string.pairing_title),
                    stringResource(R.string.pairing_confirm_hint),
                )
                Spacer(Modifier.height(8.dp))
                Text(
                    text = serverUrl,
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextMuted,
                    textAlign = TextAlign.Center,
                )
                Spacer(Modifier.height(10.dp))
                RoundAction(
                    label = if (busy) stringResource(R.string.pairing_generating)
                    else stringResource(R.string.pairing_generate),
                    enabled = !busy,
                    onClick = {
                        busy = true
                        error = null
                    },
                )
                Spacer(Modifier.height(6.dp))
                RoundAction(
                    label = stringResource(R.string.settings_server_url),
                    color = GodexColors.SurfaceHigh,
                    onClick = onSettings,
                )
            } else {
                ScreenTitle(
                    stringResource(R.string.pairing_code_title),
                    stringResource(R.string.pairing_enter_code),
                )
                Spacer(Modifier.height(6.dp))
                // Digits are grouped 4-4: an 8-digit run is easy to misread on a
                // small display, and the server normalises separators anyway.
                Text(
                    text = formatUserCode(shown.userCode),
                    style = MaterialTheme.typography.display3,
                    color = GodexColors.Accent,
                    textAlign = TextAlign.Center,
                )
                Spacer(Modifier.height(6.dp))
                status.ifEmpty { stringResource(R.string.pairing_waiting) }.let {
                    Text(
                        text = it,
                        style = MaterialTheme.typography.caption1,
                        color = GodexColors.TextSecondary,
                        textAlign = TextAlign.Center,
                    )
                }
            }

            error?.let {
                Spacer(Modifier.height(6.dp))
                MessageBlock(title = it, tint = GodexColors.Danger)
            }
        }
    }

    // Starting the pairing is a side effect, so it runs in a keyed effect rather
    // than inside onClick: a recomposition cannot mint a second code.
    LaunchedEffect(busy) {
        if (!busy || pairing != null) return@LaunchedEffect
        try {
            pairing = api.startPairing(name = "Wear Watch")
            status = context.getString(R.string.pairing_waiting)
            error = null
        } catch (err: WatchApiException) {
            error = err.message
        } catch (err: Exception) {
            error = err.message ?: context.getString(R.string.pairing_cannot_connect)
        } finally {
            // Reset on failure as well as success. Leaving it true pinned the button
            // at "生成中…" and disabled, so after a connection error the user could
            // not retry even once the address was fixed — the failure looked like a
            // hang (found on the emulator).
            busy = false
        }
    }
}

/** Renders 8 digits as `1234 5678` for legibility on a small round display. */
fun formatUserCode(code: String): String {
    val digits = code.filter { it.isDigit() }
    if (digits.length != 8) return code
    return digits.substring(0, 4) + " " + digits.substring(4)
}

private const val PAIR_POLL_INTERVAL_MS = 3000L

// 5 minutes at 3s intervals matches the server-side pairing window, so the watch
// gives up at roughly the same moment the code stops being valid.
private const val PAIR_POLL_ATTEMPTS = 100
