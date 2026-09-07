/**
 * godex Mobile 注入式任务 watcher（模板）。
 *
 * 原生层（iOS WKUserScript atDocumentStart / Android addDocumentStartJavaScript）
 * 读取本文件，替换 __GODEX_TOKEN__ 与 __GODEX_POLL_MS__ 占位符后注入目标页面，
 * 实现「Web UI 零改动」的 token 注入 + agent 任务完成本地通知。
 *
 * 注意：本文件同时以 TS 形态维护于 src/watcher.ts（buildWatcherScript），
 * 两处逻辑必须保持一致；修改时同步更新。
 */
(function () {
  var TOKEN = "__GODEX_TOKEN__";
  var POLL_MS = __GODEX_POLL_MS__;
  var NOTIFIED_KEY = "godex:notified:v1";

  // 1) token 注入：Web UI 从 localStorage["godex:web:token"] 读取并附加
  //    Authorization: Bearer 请求头（ui/web/src/store/settings.ts）。
  try {
    if (TOKEN) localStorage.setItem("godex:web:token", TOKEN);
  } catch (e) {}

  function notifiedMap() {
    try { return JSON.parse(localStorage.getItem(NOTIFIED_KEY) || "{}"); }
    catch (e) { return {}; }
  }
  function markNotified(key, state) {
    try {
      var m = notifiedMap();
      m[key] = state;
      localStorage.setItem(NOTIFIED_KEY, JSON.stringify(m));
    } catch (e) {}
  }

  function notify(title, body) {
    try {
      var cap = window.Capacitor;
      if (cap && cap.Plugins && cap.Plugins.LocalNotifications) {
        cap.Plugins.LocalNotifications.schedule({
          notifications: [{ id: Date.now() % 2147483647, title: title, body: body, sound: null }]
        }).catch(function () {});
      }
    } catch (e) {}
  }

  function authHeaders() {
    return TOKEN ? { "Authorization": "Bearer " + TOKEN } : {};
  }

  function api(path) {
    return fetch(path, { method: "GET", headers: authHeaders() })
      .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); });
  }

  function poll() {
    // 会话级完成信号：running 从 true -> false 视为一次任务结束。
    api("/api/sessions?limit=10")
      .then(function (list) {
        var sessions = Array.isArray(list) ? list : (list && list.sessions) || [];
        sessions.forEach(function (s) {
          if (!s || !s.session_id) return;
          var key = "session:" + s.session_id;
          var prev = notifiedMap()[key];
          var nowState = { running: !!s.running, ts: s.updated_at || s.last_activity_at || "" };
          if (prev && prev.running === true && !nowState.running) {
            notify("godex 任务完成", (s.title || "会话 " + s.session_id.slice(0, 8)) + " 已结束");
            nowState.notified = true;
          }
          markNotified(key, nowState);
        });
      })
      .catch(function () {});

    // longtask 级完成信号：状态进入终态（completed/error/cancelled）。
    api("/api/sessions?limit=5")
      .then(function (list) {
        var sessions = Array.isArray(list) ? list : (list && list.sessions) || [];
        sessions.forEach(function (s) {
          if (!s || !s.session_id) return;
          api("/api/sessions/" + encodeURIComponent(s.session_id) + "/longtasks")
            .then(function (tasks) {
              var arr = Array.isArray(tasks) ? tasks : [];
              arr.forEach(function (t) {
                if (!t || !t.longtask_id) return;
                var st = t.status || "";
                if (st !== "completed" && st !== "error" && st !== "canceled") return;
                var key = "longtask:" + t.longtask_id;
                var prev = notifiedMap()[key];
                if (prev === st) return;
                var title = st === "completed" ? "godex 任务完成" : "godex 任务" + (st === "error" ? "失败" : "已取消");
                notify(title, t.description || t.workflow_id || t.longtask_id.slice(0, 8));
                markNotified(key, st);
              });
            })
            .catch(function () {});
        });
      })
      .catch(function () {});
  }

  poll();
  setInterval(poll, Math.max(15000, POLL_MS || 30000));
})();
