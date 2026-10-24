package com.godex.wear.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.wear.compose.foundation.lazy.ScalingLazyColumn
import androidx.wear.compose.foundation.lazy.rememberScalingLazyListState
import androidx.wear.compose.material.ChipDefaults
import androidx.wear.compose.material.Icon
import androidx.wear.compose.material.MaterialTheme
import androidx.wear.compose.material.Text
import androidx.wear.compose.material.ToggleChip
import androidx.wear.compose.material.ToggleChipDefaults
import com.godex.wear.R
import com.godex.wear.data.Credentials
import com.godex.wear.data.WatchApi
import com.godex.wear.data.WatchPreferences

/**
 * Settings (mockup 11).
 *
 * A scrolling list rather than a fixed column: the screen carries a server
 * address field, boxed toggle chips and two full-width actions, which does not
 * fit a 454px round display at once. ScalingLazyColumn also gives rotary-input
 * support for free, which is the documented Wear pattern for lists.
 */
@Composable
fun SettingsScreen(
    credentials: Credentials?,
    preferences: WatchPreferences,
    onSaveUrl: (String) -> Unit,
    onNotifyOnComplete: (Boolean) -> Unit,
    onPowerSaving: (Boolean) -> Unit,
    onAmbient: (Boolean) -> Unit,
    onUnpair: () -> Unit,
    onBack: () -> Unit,
) {
    val current = credentials?.baseUrl?.takeIf { it.isNotEmpty() } ?: WatchApi.DEFAULT_EMULATOR_URL
    var url by remember(current) { mutableStateOf(current) }
    val listState = rememberScalingLazyListState()

    ScalingLazyColumn(
        state = listState,
        // See rememberGodexRotaryBehavior: the default behaviour crashes on
        // watches without GMS.
        rotaryScrollableBehavior = rememberGodexRotaryBehavior(listState),
        modifier = Modifier
            .fillMaxSize()
            .background(GodexColors.Background),
    ) {
        item { ScreenTitle(stringResource(R.string.settings_title)) }

        item {
            Column(modifier = Modifier.fillMaxWidth()) {
                Text(
                    text = stringResource(R.string.settings_server_url),
                    style = MaterialTheme.typography.caption1,
                    color = GodexColors.TextSecondary,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(4.dp))
                BasicTextField(
                    value = url,
                    onValueChange = { url = it },
                    singleLine = true,
                    textStyle = TextStyle(color = GodexColors.TextPrimary, textAlign = TextAlign.Center),
                    cursorBrush = SolidColor(GodexColors.Accent),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(GodexColors.Surface)
                        .padding(8.dp),
                )
                Spacer(Modifier.height(6.dp))
                SmallAction(
                    label = stringResource(R.string.settings_save_url),
                    highlight = true,
                    onClick = { onSaveUrl(url) },
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }

        // ---- 通知 ----
        item {
            Text(
                text = stringResource(R.string.settings_notifications),
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextSecondary,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }

        item {
            SettingToggle(
                checked = preferences.notifyOnComplete,
                label = stringResource(R.string.settings_notify_complete),
                secondary = stringResource(R.string.settings_notify_complete_desc),
                onCheckedChange = onNotifyOnComplete,
            )
        }

        // Rendered as plain text, not a disabled switch: a greyed-out control
        // invites the user to try to enable it. Stating the rule avoids the
        // question, and it is true — the approvals channel (mockup 08) cannot be
        // turned off while an agent is blocked on it.
        item {
            Text(
                text = stringResource(R.string.settings_approvals_forced),
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
            )
        }

        // ---- 省电 ----
        item {
            Text(
                text = stringResource(R.string.settings_power),
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextSecondary,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }

        item {
            SettingToggle(
                checked = preferences.powerSaving,
                label = stringResource(R.string.settings_power_desc),
                secondary = stringResource(R.string.settings_power_desc2),
                onCheckedChange = onPowerSaving,
            )
        }

        item {
            SettingToggle(
                checked = preferences.ambient,
                label = stringResource(R.string.settings_ambient),
                secondary = stringResource(R.string.settings_ambient_desc),
                onCheckedChange = onAmbient,
            )
        }

        // ---- 安全 ----
        item {
            Text(
                text = stringResource(R.string.settings_security),
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextSecondary,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth(),
            )
        }

        item {
            // Showing the granted scopes makes an under-privileged pairing
            // diagnosable on the wrist instead of needing server logs.
            val scopes = credentials?.scopes.orEmpty()
            Text(
                text = stringResource(R.string.settings_scopes) +
                    if (scopes.isEmpty()) stringResource(R.string.tile_unpaired)
                    else scopes.sorted().joinToString(" · "),
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                textAlign = TextAlign.Center,
                maxLines = 4,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.fillMaxWidth().padding(vertical = 4.dp),
            )
        }

        item {
            Column(modifier = Modifier.fillMaxWidth()) {
                SmallAction(
                    label = stringResource(R.string.settings_unpair),
                    onClick = onUnpair,
                    modifier = Modifier.fillMaxWidth(),
                )
                Spacer(Modifier.height(6.dp))
                SmallAction(
                    label = stringResource(R.string.common_back),
                    onClick = onBack,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
        }
    }
}

/**
 * One settings switch.
 *
 * The secondary line carries the consequence of the switch ("含结果摘要"), not a
 * restatement of its name — the point of the row is to let the user predict what
 * changing it will do without turning it on first.
 */
@Composable
private fun SettingToggle(
    checked: Boolean,
    label: String,
    secondary: String,
    onCheckedChange: (Boolean) -> Unit,
) {
    ToggleChip(
        checked = checked,
        onCheckedChange = onCheckedChange,
        label = {
            Text(
                text = label,
                style = MaterialTheme.typography.body1,
                color = GodexColors.TextPrimary,
                maxLines = 1,
            )
        },
        // toggleControl is a required parameter (no default) in Wear Material:
        // the chip must always state what kind of control it is, so the icon is
        // not optional. switchIcon renders the on/off switch the mockup uses.
        toggleControl = {
            Icon(
                imageVector = ToggleChipDefaults.switchIcon(checked),
                contentDescription = if (checked) stringResource(R.string.common_on)
                else stringResource(R.string.common_off),
            )
        },
        secondaryLabel = {
            Text(
                text = secondary,
                style = MaterialTheme.typography.caption1,
                color = GodexColors.TextMuted,
                maxLines = 2,
                overflow = TextOverflow.Ellipsis,
            )
        },
        colors = ToggleChipDefaults.toggleChipColors(
            checkedStartBackgroundColor = GodexColors.Accent,
            checkedEndBackgroundColor = GodexColors.Accent,
            uncheckedStartBackgroundColor = GodexColors.Surface,
            uncheckedEndBackgroundColor = GodexColors.Surface,
            checkedContentColor = GodexColors.Background,
            uncheckedContentColor = GodexColors.TextPrimary,
            // The toggle control itself stays readable in both states; a control
            // that disappears against the checked background reads as decoration.
            checkedToggleControlColor = GodexColors.Background,
            uncheckedToggleControlColor = GodexColors.TextMuted,
        ),
        modifier = Modifier.fillMaxWidth(),
    )
}
