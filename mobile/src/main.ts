import { Preferences } from "@capacitor/preferences";
import { LocalNotifications } from "@capacitor/local-notifications";
import { StatusBar } from "@capacitor/status-bar";

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
