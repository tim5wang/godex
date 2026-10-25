import { Preferences } from "@capacitor/preferences";
import { LocalNotifications } from "@capacitor/local-notifications";
import { StatusBar } from "@capacitor/status-bar";
import { CapacitorHttp } from "@capacitor/core";

const K = {
  server: "godex_server_url",
  token: "godex_token",
  pollMs: "godex_poll_ms",
  localMode: "godex_local_mode",
  localError: "godex_local_error",
} as const;

function $(id: string): HTMLInputElement {
  return document.getElementById(id) as HTMLInputElement;
}

const serverInput = $("server");
const tokenInput = $("token");
const intervalInput = $("interval");
const connectBtn = $("connect");
const pairCodeInput = $("paircode");
const pairApproveBtn = $("pairapprove");
const pairStatusEl = document.getElementById("pairstatus") as HTMLDivElement;
const statusEl = $("status");

function setStatus(text: string, ok?: boolean) {
  statusEl.textContent = text;
  statusEl.className = ok ? "ok" : "err";
}

function normalizeServer(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, "");
  if (!/^https?:\/\//i.test(trimmed)) {
    return `http://${trimmed}`;
  }
  return trimmed;
}

async function loadStored() {
  try {
    const [server, token, pollMs, localError] = await Promise.all([
      Preferences.get({ key: K.server }),
      Preferences.get({ key: K.token }),
      Preferences.get({ key: K.pollMs }),
      Preferences.get({ key: K.localError }),
    ]);
    if (server.value) serverInput.value = server.value;
    if (token.value) tokenInput.value = token.value;
    if (pollMs.value) intervalInput.value = String(Math.round(Number(pollMs.value) / 1000));
    if (localError.value) {
      setStatus("本地 agent 启动失败：" + localError.value.slice(0, 300), false);
    }
  } catch (e) {
    console.warn("load preferences failed", e);
  }
}

async function requestNotifyPermission(): Promise<boolean> {
  try {
    const perm = await LocalNotifications.requestPermissions();
    return perm.display === "granted";
  } catch (e) {
    console.warn("notification permission failed", e);
    return false;
  }
}

async function onConnect() {
  const server = normalizeServer(serverInput.value);
  if (!server) {
    setStatus("请填写 godex 服务地址");
    return;
  }
  const token = tokenInput.value.trim();
  const pollSec = Math.max(15, Number(intervalInput.value) || 30);
  const pollMs = pollSec * 1000;

  connectBtn.disabled = true;
  setStatus("正在请求通知权限…");

  const granted = await requestNotifyPermission();
  if (!granted) {
    setStatus("通知权限未授予：将无法收到任务完成提醒（仍可正常使用）");
  } else {
    setStatus("通知权限已就绪");
  }

  // 持久化配置，原生层在 WebView 加载目标页时注入 watcher。
  await Promise.all([
    Preferences.set({ key: K.server, value: server }),
    Preferences.set({ key: K.token, value: token }),
    Preferences.set({ key: K.pollMs, value: String(pollMs) }),
  ]);

  setStatus(`正在连接 ${server} …`);
  try {
    await StatusBar.hide();
  } catch {
    /* 非必须 */
  }
  // 导航到目标 godex Web UI；原生 WKUserScript / document-start 脚本会在
  // 目标页 origin 注入 token + 任务完成轮询 watcher。
  window.location.href = server;
}

connectBtn.addEventListener("click", onConnect);

// ---- Watch pairing (trusted side) ----

/**
 * Strips separators from a typed code.
 *
 * The watch renders 8 digits as "1234 5678" because an unbroken run is easy to
 * misread on a small display, and users copy it verbatim. The node normalises
 * separators too, but sending the cleaned form keeps the request self-evident.
 */
function normalizePairCode(raw: string): string {
  return raw.replace(/\D/g, "");
}

function setPairStatus(text: string, ok?: boolean) {
  pairStatusEl.textContent = text;
  pairStatusEl.className = ok ? "ok" : "err";
}

/**
 * Approves the pairing code shown on the watch.
 *
 * Uses CapacitorHttp rather than fetch: the node sends no CORS headers (checked
 * against the running server — no Access-Control-Allow-Origin anywhere in the
 * Go source), so a cross-origin fetch from this page's origin would be blocked by
 * the WebView before the request left the device. The native HTTP bridge issues
 * the same POST outside the browser's CORS enforcement, which is the supported
 * way to call a LAN API from a Capacitor shell.
 */
async function onApprovePair() {
  const code = normalizePairCode(pairCodeInput.value);
  if (code.length !== 8) {
    setPairStatus("请输入手表上显示的 8 位配对码", false);
    return;
  }
  const server = normalizeServer(serverInput.value);
  if (!server) {
    setPairStatus("请先填写 godex 服务地址", false);
    return;
  }
  const token = tokenInput.value.trim();
  if (!token) {
    // The node rejects an unauthenticated approve, and "401" alone would read as
    // "wrong code". Say what is actually missing: approving requires the node's
    // own token, which is what makes this page a trusted endpoint.
    setPairStatus("批准手表需要先填写访问令牌", false);
    return;
  }

  pairApproveBtn.disabled = true;
  setPairStatus("正在批准…");
  try {
    const res = await CapacitorHttp.post({
      url: `${server}/api/watch/pair/approve`,
      headers: {
        "Content-Type": "application/json",
        Authorization: `Bearer ${token}`,
      },
      // Explicit scopes: an empty list makes the node fall back to its full
      // default set, which would grant more than the user sees here.
      data: { user_code: code, scopes: ["watch:read", "watch:command", "watch:approve"] },
      // The node serialises JSON; without this Capacitor may send the object as a
      // form body, which the server answers with 400.
      responseType: "json",
    });
    const body = typeof res.data === "string" ? safeJSON(res.data) : res.data;
    if (res.status >= 200 && res.status < 300) {
      const name = (body && (body.name || body.device_id)) || "手表";
      setPairStatus(`已批准：${name}。手表会自动进入已配对状态。`, true);
      pairCodeInput.value = "";
      // Persist server + token so 「连接并进入」 works immediately afterwards
      // without retyping the address the user just proved is reachable.
      await Promise.all([
        Preferences.set({ key: K.server, value: server }),
        Preferences.set({ key: K.token, value: token }),
      ]);
    } else {
      // Surface the node's own message: "expired" and "wrong code" need
      // different fixes, and a generic failure would hide which one it is.
      const msg = (body && (body.error || body.message)) || `HTTP ${res.status}`;
      setPairStatus(`批准失败：${msg}`, false);
    }
  } catch (e) {
    setPairStatus(`批准失败：${e instanceof Error ? e.message : String(e)}`, false);
  } finally {
    pairApproveBtn.disabled = false;
  }
}

function safeJSON(text: string): any {
  try {
    return JSON.parse(text);
  } catch {
    return null;
  }
}

pairApproveBtn.addEventListener("click", onApprovePair);
// Groups the code as it is typed, matching how the watch displays it.
pairCodeInput.addEventListener("input", () => {
  const digits = normalizePairCode(pairCodeInput.value).slice(0, 8);
  pairCodeInput.value = digits.length > 4 ? `${digits.slice(0, 4)} ${digits.slice(4)}` : digits;
});

// 已配置过时自动进入（避免每次冷启动都停在设置页）。
// 注意：本地模式下 MainActivity 会在 godex serve 就绪后自行 loadUrl 导航，
// 设置页不应抢先跳转（服务未就绪会连不上），故本地模式只显示启动状态。
loadStored().then(async () => {
  try {
    const localMode = await Preferences.get({ key: K.localMode });
    if (localMode.value === "true") {
      setStatus("本地 agent 启动中…（godex serve 由 App 内置拉起）");
      return;
    }
  } catch (e) {
    /* 非本地模式，走远程逻辑 */
  }
  const server = serverInput.value;
  if (server) {
    await requestNotifyPermission();
    window.location.href = server;
  }
});
