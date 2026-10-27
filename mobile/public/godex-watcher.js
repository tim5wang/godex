/**
 * godex Mobile 注入式任务 watcher（模板）。
 *
 * 原生层（iOS WKUserScript atDocumentStart / Android addDocumentStartJavaScript）
 * 读取本文件，替换 __GODEX_TOKEN__ 与 __GODEX_POLL_MS__ 占位符后注入目标页面，
 * 实现「Web UI 零改动」的 token 注入 + agent 任务完成本地通知。
 *
 * 注意：本文件是**唯一源**；mobile/www、mobile/ios/.../public、
 * mobile/android/.../assets/public 下的同名文件都是构建产物（已被 .gitignore 忽略）。
 */
(function () {
  var TOKEN = "__GODEX_TOKEN__";
  var POLL_MS = __GODEX_POLL_MS__;
  var NOTIFIED_KEY = "godex:notified:v1";
  // Push-queue cursor (W5). Persisted so a phone that was closed does not
  // re-alert everything it had already shown.
  var PUSH_CURSOR_KEY = "godex:push:cursor:v1";

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

  function pushCursor() {
    try { return parseInt(localStorage.getItem(PUSH_CURSOR_KEY) || "0", 10) || 0; }
    catch (e) { return 0; }
  }
  function setPushCursor(value) {
    try { localStorage.setItem(PUSH_CURSOR_KEY, String(value)); } catch (e) {}
  }

  /**
   * `extra` carries the deep link the watch uses to land on the right screen.
   *
   * The keys must be the exact strings the watch reads (WearNotifier's
   * EXTRA_SESSION_ID = "godex_session_id", EXTRA_APPROVAL_KEY =
   * "godex_approval_key"): an approval alert whose key is named "approval_key"
   * would route to the session detail instead, which is the very defect A1 fixed
   * on the watch side.
   *
   * Caveat: on Android the bridge to the watch is the platform's own notification
   * bridging, and whether these extras survive onto the wrist is a real-device
   * question (gate 0.4–0.6), not something this file can prove.
   */
  function notify(title, body, extra) {
    try {
      var cap = window.Capacitor;
      if (cap && cap.Plugins && cap.Plugins.LocalNotifications) {
        cap.Plugins.LocalNotifications.schedule({
          notifications: [{
            id: Date.now() % 2147483647,
            title: title,
            body: body,
            sound: null,
            extra: extra || {}
          }]
        }).catch(function () {});
      }
    } catch (e) {}
  }

  function authHeaders() {
    return TOKEN ? { "Authorization": "Bearer " + TOKEN } : {};
  }

  /**
   * 可见失败提示（A2）。
   *
   * 之前这里所有失败都被 `.catch(function () {})` 吞掉。对「网络抖动」那样做是对的，
   * 但 403/401 不是抖动——它们是**稳定事实**：令牌无权读本机会话 / 令牌无效。
   *
   * 触发场景真实存在：设置页允许粘贴任意令牌，而用户可以拿租户账号的 session token
   * 来连（新账号体系让它更可能）。Web UI 里 /sessions 会被 requireCenterExecution 拒
   * 成 403 并在会话栏给出提示，但**原生通知**不会：用户只看到「没有通知」，无从知道
   * 是没任务还是没权限。这里把同一事实搬到页面上。
   */
  var NOTICE_ID = "godex-watcher-notice";
  // 措辞按实测校准：租户令牌下 /api/sessions 是 403，但 /api/watch/push/pending
  // 是 200（实测 2026-10-28：tenant token → sessions=403, push/pending=200）。
  // 所以停的是「任务完成提醒」（本文件轮询 /api/sessions 的那两段），
  // 而审批/Flow 人工卡点推送仍照常——说成「通知全部停了」就是夸大。
  var NOTICE_TEXT = {
    refused: "godex 任务完成提醒已暂停：当前令牌无权读取本机会话（HTTP 403），任务结束不会弹通知（审批推送不受影响）。请在设置页改用超管共享令牌，或改用有权限的账号。",
    unauthorized: "godex 任务完成提醒已暂停：令牌无效或已过期（HTTP 401）。请在设置页重新连接。"
  };

  function noticeEl() {
    if (typeof document === "undefined" || !document) return null;
    try { return document.getElementById(NOTICE_ID); } catch (e) { return null; }
  }

  function mountNotice(text) {
    // typeof check rather than relying on a thrown ReferenceError: this file also
    // runs in injection harnesses and non-browser contexts, and using exceptions as
    // control flow there would look identical to the silent failure being fixed.
    if (typeof document === "undefined" || !document) return;
    try {
      if (!document.body) {
        // 本脚本由原生层在 document-start 注入，此时 body 还不存在。
        // 直接 appendChild 会抛错并被 catch 掉——那就又变成静默了，
        // 所以等到 DOM 可用再挂。
        if (document.addEventListener) {
          document.addEventListener("DOMContentLoaded", function () { mountNotice(text); });
        }
        return;
      }
      var el = noticeEl();
      if (!el) {
        el = document.createElement("div");
        el.id = NOTICE_ID;
        // 内联样式：这是注入到任意页面（可能是旧版 Web UI）的脚本，
        // 不能依赖页面里存在某个样式表。
        el.setAttribute("style", [
          "position:fixed", "left:0", "right:0", "bottom:0", "z-index:2147483647",
          "background:#b42318", "color:#fff", "font:13px/1.5 -apple-system,sans-serif",
          "padding:10px 14px", "box-shadow:0 -2px 8px rgba(0,0,0,.25)"
        ].join(";"));
        document.body.appendChild(el);
      }
      el.textContent = text;
    } catch (e) {}
  }

  /** 成功即说明权限正常，提示自动消失；这样换回有权限的令牌后不会留一条陈旧横幅。 */
  function clearNotice() {
    var el = noticeEl();
    if (!el) return;
    try { el.textContent = ""; } catch (e) {}
    try {
      if (el.parentNode && el.parentNode.removeChild) el.parentNode.removeChild(el);
      else if (el.remove) el.remove();
    } catch (e) {}
  }

  /**
   * 失败分类：只把「鉴权被拒」变成可见提示。
   *
   * 网络抖动 / 5xx 保持静默——手机经常断网，把它们也弹出来只会变成误报噪音，
   * 反而淹没真正需要用户动作的那一条。
   */
  function noteFailure(err) {
    var status = err && err.status;
    if (status === 403) mountNotice(NOTICE_TEXT.refused);
    else if (status === 401) mountNotice(NOTICE_TEXT.unauthorized);
  }

  function api(path) {
    return fetch(path, { method: "GET", headers: authHeaders() })
      .then(function (r) {
        if (!r.ok) {
          // 带上状态码：调用方要能区分「被拒」（403/401）与「暂时不通」。
          var err = new Error("HTTP " + r.status);
          err.status = r.status;
          throw err;
        }
        return r.json();
      });
  }

  function poll() {
    // 会话级完成信号：running 从 true -> false 视为一次任务结束。
    api("/api/sessions?limit=10")
      .then(function (list) {
        clearNotice();
        var sessions = Array.isArray(list) ? list : (list && list.sessions) || [];
        sessions.forEach(function (s) {
          if (!s || !s.session_id) return;
          var key = "session:" + s.session_id;
          var prev = notifiedMap()[key];
          var nowState = { running: !!s.running, ts: s.updated_at || s.last_activity_at || "" };
          if (prev && prev.running === true && !nowState.running) {
            notify("godex 任务完成", (s.title || "会话 " + s.session_id.slice(0, 8)) + " 已结束", {
              godex_session_id: s.session_id
            });
            nowState.notified = true;
          }
          markNotified(key, nowState);
        });
      })
      .catch(noteFailure);

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
                notify(title, t.description || t.workflow_id || t.longtask_id.slice(0, 8), {
                  godex_session_id: s.session_id
                });
                markNotified(key, st);
              });
            })
            .catch(noteFailure);
        });
      })
      .catch(noteFailure);
  }

  /**
   * W5 推送队列：服务端把「待推送事件」放进有界队列，手机拉取后投递为本地通知，
   * Android 再桥接到配对手表（无 FCM 时的唯一通路）。
   *
   * 与上面两段轮询并存：那些是客户端自己判定的完成信号，这里是服务端派生的事件
   * （审批请求 / Flow 人工卡点），两者互补。
   */
  function pushPoll(since) {
    var cursor = typeof since === "number" ? since : pushCursor();
    api("/api/watch/push/pending?since=" + cursor)
      .then(function (res) {
        var events = (res && res.events) || [];
        var serverCursor = (res && res.cursor) || 0;
        if (typeof res !== "undefined" && res && serverCursor < cursor) {
          // The node restarted: its sequence restarted too, so the stored cursor
          // points past every future event and would silence the queue forever.
          // Reset and re-read instead of skipping notifications silently.
          setPushCursor(0);
          pushPoll(0);
          return;
        }
        events.forEach(function (e) {
          if (!e || !e.id) return;
          notify(e.title || "godex 通知", e.body || "", {
            godex_device_id: e.device_id || "",
            godex_session_id: e.session_id || "",
            // The watch reads this as EXTRA_APPROVAL_KEY to route straight to the
            // approval screen (mockup 06) rather than the session detail.
            godex_approval_key: e.request_id || "",
            godex_event_id: e.id,
            godex_event_kind: e.kind || ""
          });
        });
        setPushCursor(serverCursor);
      })
      .catch(function () {});
  }

  poll();
  pushPoll();
  setInterval(function () {
    poll();
    pushPoll();
  }, Math.max(15000, POLL_MS || 30000));

  // Exposed for the offline harness (and for manual debugging in a WebView).
  // Harmless in production, and it keeps the verification honest: the test drives
  // the same function the interval calls instead of re-implementing it.
  try {
    window.__godexWatcher = {
      poll: poll,
      pushPoll: pushPoll,
      pushCursor: pushCursor,
      // 供离线装置断言「提示是否可见」，避免测试重写一遍挂载逻辑。
      noticeText: function () {
        var el = noticeEl();
        return el ? el.textContent || "" : "";
      }
    };
  } catch (e) {}
})();
