package com.godex.mobile;

import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

/**
 * Native navigation for the remote godex Web UI.
 *
 * Why this exists: Capacitor intercepts page-initiated navigations in
 * `Bridge.launchIntent()` and hands any URL whose host is neither the app's own
 * host nor listed in `server.allowNavigation` to the system browser via
 * `Intent.ACTION_VIEW`. So the previous `window.location.href = server` opened
 * Chrome instead of loading the UI in-app — the setup page could never actually
 * reach its target.
 *
 * `allowNavigation` is not the fix either: its entries are also registered as
 * authorities of Capacitor's local asset server, so a path that exists in
 * `www/` (notably `/` and therefore `index.html`) would be answered with the
 * bundled setup page instead of the node's real Web UI.
 *
 * Loading through `WebView.loadUrl()` skips `shouldOverrideUrlLoading`
 * entirely, which is why local mode already navigated correctly that way.
 */
@CapacitorPlugin(name = "GodexNav")
public class GodexNavPlugin extends Plugin {

    /**
     * Loads the godex Web UI at [url] inside the app's WebView.
     *
     * The token is re-injected before navigating: the document-start watcher is
     * what authorises the Web UI's API calls and drives task-completion
     * notifications, and a token edited on the setup page would otherwise be
     * ignored until the next cold start.
     */
    @PluginMethod
    public void open(PluginCall call) {
        String url = call.getString("url");
        if (url == null || url.trim().isEmpty()) {
            call.reject("url is required");
            return;
        }
        String token = call.getString("token", "");
        MainActivity activity = (MainActivity) getActivity();
        if (activity == null) {
            call.reject("no activity");
            return;
        }
        activity.openServerUrl(url.trim(), token == null ? "" : token);
        JSObject ret = new JSObject();
        ret.put("url", url);
        call.resolve(ret);
    }

    /** Returns to the bundled setup page (used when the node is unreachable). */
    @PluginMethod
    public void openSetup(PluginCall call) {
        MainActivity activity = (MainActivity) getActivity();
        if (activity == null) {
            call.reject("no activity");
            return;
        }
        activity.openSetupPage();
        call.resolve();
    }
}
