package com.godex.mobile;

/**
 * Host of the godex node the WebView is currently pointed at.
 *
 * Lives here rather than being passed to every call because the WebView client is
 * created with the Bridge and receives only a `WebResourceRequest`; the decision it
 * has to make ("is this the node we are displaying?") depends on state that is set
 * later, when the user connects or the app restores its saved address.
 *
 * Deliberately a single static field rather than per-activity state: there is one
 * WebView and one node at a time, and a stale value would only mean keeping one
 * extra host inside the WebView, which the user explicitly navigated to anyway.
 */
final class NodeHostHolder {

    private static volatile String nodeHost = "";

    private NodeHostHolder() {
    }

    static void set(String host) {
        nodeHost = host == null ? "" : host;
    }

    static String get() {
        return nodeHost;
    }
}
