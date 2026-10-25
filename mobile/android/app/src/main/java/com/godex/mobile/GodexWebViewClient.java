package com.godex.mobile;

import android.net.Uri;
import android.webkit.WebResourceRequest;
import android.webkit.WebView;

import com.getcapacitor.Bridge;
import com.getcapacitor.BridgeWebViewClient;

/**
 * Keeps navigation to the configured godex node inside the app's WebView.
 *
 * Capacitor's `Bridge.launchIntent()` hands any URL whose host is neither the
 * app's own host nor in `server.allowNavigation` to the system browser. That is
 * correct for external links, but it also catches two things the app needs to keep:
 *
 *  1. The initial `loadUrl()` of the node — avoided by navigating natively
 *     (GodexNavPlugin) instead of setting `window.location.href`.
 *  2. **Redirects performed by the node itself.** Measured: the public node answers
 *     `http://godex.claw.carc.top/` with `308 Permanent Redirect` to its https
 *     origin. A redirect is delivered through `shouldOverrideUrlLoading` exactly
 *     like a page-initiated navigation, so even after the native load the browser
 *     opened the https URL and the app never displayed the UI.
 *
 * So this client keeps same-host navigations in the WebView — regardless of scheme,
 * because the http->https upgrade is the same node — and defers everything else to
 * Capacitor so genuine external links still leave the app.
 */
public class GodexWebViewClient extends BridgeWebViewClient {

    public GodexWebViewClient(Bridge bridge) {
        super(bridge);
    }

    @Override
    public boolean shouldOverrideUrlLoading(WebView view, WebResourceRequest request) {
        Uri url = request.getUrl();
        String nodeHost = NodeHostHolder.get();
        String host = url == null ? null : url.getHost();
        if (nodeHost != null && !nodeHost.isEmpty() && nodeHost.equalsIgnoreCase(host)) {
            // false = let the WebView follow it (this also covers the 308 redirect).
            return false;
        }
        return super.shouldOverrideUrlLoading(view, request);
    }
}
