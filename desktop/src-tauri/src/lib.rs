// godex desktop shell — Tauri 2 (M1 MVP + M2 native capabilities, self-hosted).
//
// The shell embeds the Go-built godex binary as a sidecar. On launch it
// spawns `godex serve` on a local port (default 127.0.0.1:17889, an
// alternate free port is chosen if 17889 is taken), injects a generated web
// token into the child env and the WebView localStorage, then loads the
// local web UI in a single window with a system tray (open/quit),
// close-to-tray, single instance, system notifications bridged from the
// backend task-completion SSE stream (/api/desktop/events), a configurable
// global shortcut (default Cmd/Ctrl+Shift+G) and clipboard support.
//
// Setting GODEX_DESKTOP_URL switches to external mode: the shell connects
// to an already-running godex server instead of spawning its own.

use std::io::{BufRead, BufReader};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{Manager, RunEvent, WebviewUrl, WebviewWindow, WebviewWindowBuilder};
use tauri_plugin_global_shortcut::{Code, GlobalShortcutExt, Modifiers, Shortcut, ShortcutState};
use tauri_plugin_notification::NotificationExt;
use tauri_plugin_shell::process::{CommandChild, CommandEvent};
use tauri_plugin_shell::ShellExt;

// Picks a less collision-prone listen port: 17889 (godex 谐音) by default
// instead of 8080, which is far too commonly occupied by other tools.
const DEFAULT_PORT: u16 = 17889;
const DEFAULT_HOTKEY: &str = "CmdOrCtrl+Shift+G";
const MAIN_WINDOW: &str = "main";

/// Managed handle to the spawned godex serve child process (self-hosted mode).
struct ServerChild(Mutex<Option<CommandChild>>);

fn env_or(name: &str, default: &str) -> String {
    std::env::var(name).ok().filter(|s| !s.trim().is_empty()).unwrap_or_else(|| default.to_string())
}

fn web_token_env() -> Option<String> {
    std::env::var("GODEX_WEB_TOKEN").ok().filter(|s| !s.trim().is_empty()).map(|s| s.trim().to_string())
}

fn hotkey_spec() -> String {
    env_or("GODEX_DESKTOP_HOTKEY", DEFAULT_HOTKEY)
}

fn show_main(app: &tauri::AppHandle) {
    if let Some(w) = app.get_webview_window(MAIN_WINDOW) {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
    }
}

fn focus_main(app: &tauri::AppHandle) {
    show_main(app);
}

// ---- Self-hosted server management ----

/// Picks the listen port: GODEX_DESKTOP_PORT if set, else 17889 when free,
/// else the first free ephemeral port.
fn pick_port() -> u16 {
    if let Ok(raw) = std::env::var("GODEX_DESKTOP_PORT") {
        if let Ok(port) = raw.trim().parse::<u16>() {
            return port;
        }
    }
    if std::net::TcpListener::bind(("127.0.0.1", DEFAULT_PORT)).is_ok() {
        return DEFAULT_PORT;
    }
    if let Ok(listener) = std::net::TcpListener::bind(("127.0.0.1", 0)) {
        if let Ok(addr) = listener.local_addr() {
            return addr.port();
        }
    }
    DEFAULT_PORT
}

/// Generates a random hex web token for the child server.
fn gen_token() -> Result<String, String> {
    let mut buf = [0u8; 16];
    getrandom::getrandom(&mut buf).map_err(|e| e.to_string())?;
    Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}

/// Spawns `godex serve` (embedded sidecar) and waits until /meta responds.
/// Returns (base_url, token, child).
fn spawn_server(app: &tauri::AppHandle) -> Result<(String, Option<String>, CommandChild), String> {
    let port = pick_port();
    let addr = format!("127.0.0.1:{port}");
    let token = match web_token_env() {
        Some(t) => Some(t),
        None => Some(gen_token()?),
    };

    let workspace = env_or("GODEX_DESKTOP_WORKSPACE", &env_or("HOME", "."));

    let sidecar = app
        .shell()
        .sidecar("godex")
        .map_err(|e| format!("locate embedded godex: {e}"))?;

    let mut cmd = sidecar
        .args(["serve", "--addr", &addr])
        .current_dir(workspace);
    if let Some(tok) = &token {
        cmd = cmd.env("GODEX_WEB_TOKEN", tok);
    }

    let (mut rx, child) = cmd.spawn().map_err(|e| format!("spawn godex serve: {e}"))?;

    // Drain the child's stdout/stderr to logs so it never blocks on a full pipe.
    let handle = app.clone();
    tauri::async_runtime::spawn(async move {
        while let Some(event) = rx.recv().await {
            match event {
                CommandEvent::Stdout(line) => {
                    eprintln!("[godex] {}", String::from_utf8_lossy(&line));
                }
                CommandEvent::Stderr(line) => {
                    eprintln!("[godex] {}", String::from_utf8_lossy(&line));
                }
                CommandEvent::Terminated(payload) => {
                    eprintln!("[godex] serve terminated: {:?}", payload.code);
                    break;
                }
                _ => {}
            }
        }
        let _ = handle;
    });

    // Wait for the server to come up.
    let url = format!("http://{addr}");
    let deadline = std::time::Instant::now() + Duration::from_secs(20);
    loop {
        match ureq::get(&format!("{url}/meta")).call() {
            Ok(resp) if resp.status() == 200 => break,
            _ => {
                if std::time::Instant::now() >= deadline {
                    return Err(format!("godex serve did not become ready at {url}"));
                }
                std::thread::sleep(Duration::from_millis(300));
            }
        }
    }

    Ok((url, token, child))
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            // Second launch: focus the existing window instead of forking.
            show_main(app);
        }))
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_clipboard_manager::init())
        .plugin(tauri_plugin_global_shortcut::Builder::new().build())
        .plugin(tauri_plugin_shell::init())
        .manage(ServerChild(Mutex::new(None)))
        .setup(|app| {
            // External mode: connect to an already-running godex server.
            let external = std::env::var("GODEX_DESKTOP_URL")
                .ok()
                .filter(|s| !s.trim().is_empty())
                .map(|s| s.trim_end_matches('/').to_string());

            let (url, token, child) = match external {
                Some(u) => (u, web_token_env(), None),
                None => match spawn_server(app.handle()) {
                    Ok((url, token, child)) => (url, token, Some(child)),
                    Err(e) => {
                        eprintln!("godex-desktop: failed to start embedded godex serve: {e}");
                        // Fall back to external URL so the window can at least
                        // show an error/placeholder when a server exists.
                        let fallback = env_or("GODEX_DESKTOP_URL", "http://127.0.0.1:17889");
                        (fallback, web_token_env(), None)
                    }
                },
            };
            if let Some(child) = child {
                if let Some(state) = app.try_state::<ServerChild>() {
                    *state.0.lock().unwrap() = Some(child);
                }
            }

            // Build the main window that loads the local godex web UI.
            let mut builder = WebviewWindowBuilder::new(
                app,
                MAIN_WINDOW,
                WebviewUrl::External(url.parse::<tauri::Url>().map_err(|e| e.to_string())?),
            )
            .title("godex")
            .inner_size(1280.0, 860.0)
            .min_inner_size(800.0, 560.0);

            // R2 mitigation: inject the web token into localStorage before the
            // UI boots so the SPA can authenticate against the local API.
            if let Some(tok) = &token {
                let js = format!("window.localStorage.setItem('godex:web:token', {tok:?});");
                builder = builder.initialization_script(js);
            }
            let window = builder.build()?;

            setup_tray(app.handle(), &window);
            setup_global_shortcut(app.handle());
            spawn_event_bridge(app.handle().clone(), url, token);

            Ok(())
        })
        // Close-to-tray (R6): closing the window keeps the service and tray
        // alive; the process exits only via the tray "Quit" menu item.
        .on_window_event(|window, event| {
            if let tauri::WindowEvent::CloseRequested { api, .. } = event {
                api.prevent_close();
                let _ = window.hide();
            }
        })
        .build(tauri::generate_context!())
        .expect("error while building godex desktop shell")
        .run(|app, event| {
            // Reap the embedded server child when the app finally exits.
            if let RunEvent::Exit = event {
                if let Some(state) = app.try_state::<ServerChild>() {
                    if let Ok(mut guard) = state.0.lock() {
                        if let Some(child) = guard.take() {
                            let _ = child.kill();
                        }
                    }
                }
            }
        });
}

// ---- Tray ----

// Tray setup is best-effort: a failure (menu/icon/build error) must only be
// logged, never propagated — an Err from the setup hook panics inside Tauri's
// AppKit callback and aborts the whole app.
fn setup_tray(app: &tauri::AppHandle, window: &WebviewWindow) {
    let result = (|| -> tauri::Result<()> {
        let show = MenuItem::with_id(app, "show", "打开主窗口", true, None::<&str>)?;
        let quit = MenuItem::with_id(app, "quit", "退出", true, None::<&str>)?;
        let menu = Menu::with_items(app, &[&show, &quit])?;

        let Some(icon) = app.default_window_icon() else {
            return Ok(()); // No bundled icon — skip the tray silently.
        };

        let window = window.clone();
        TrayIconBuilder::with_id("godex-tray")
            .icon(icon.clone())
            .tooltip("godex")
            .menu(&menu)
            .show_menu_on_left_click(false)
            .on_menu_event(move |app, event| match event.id().as_ref() {
                "show" => {
                    let _ = window.show();
                    let _ = window.unminimize();
                    let _ = window.set_focus();
                }
                "quit" => {
                    app.exit(0);
                }
                _ => {}
            })
            .on_tray_icon_event(|tray, event| {
                if let TrayIconEvent::Click {
                    button: MouseButton::Left,
                    button_state: MouseButtonState::Up,
                    ..
                } = event
                {
                    let app = tray.app_handle();
                    show_main(app);
                }
            })
            .build(app)?;
        Ok(())
    })();
    if let Err(e) = result {
        eprintln!("godex-desktop: tray setup skipped ({e})");
    }
}

// ---- Global shortcut ----

// Registers the global hotkey. Registration can fail (e.g. the combination is
// already taken by another app on macOS), which MUST NOT abort the shell: log
// and continue. The hotkey is an optional convenience, not a core capability.
fn setup_global_shortcut(app: &tauri::AppHandle) {
    let spec = hotkey_spec();
    let shortcut: Shortcut = spec
        .parse()
        .unwrap_or_else(|_| Shortcut::new(Some(Modifiers::SHIFT | Modifiers::CONTROL), Code::KeyG));

    let result = app
        .global_shortcut()
        .on_shortcut(shortcut, |app, _shortcut, event| {
            if event.state == ShortcutState::Pressed {
                focus_main(app);
            }
        })
        .and_then(|_| app.global_shortcut().register(shortcut));
    if let Err(e) = result {
        eprintln!("godex-desktop: global shortcut registration skipped ({e}); \
                   use GODEX_DESKTOP_HOTKEY to pick a free combination");
    }
}

// ---- System notification bridge ----
//
// Subscribes to the backend SSE stream /api/desktop/events and raises a
// system notification when an agent task transitions running → idle.

fn spawn_event_bridge(app: tauri::AppHandle, base: String, token: Option<String>) {
    std::thread::spawn(move || {
        let app = Arc::new(app);
        loop {
            let endpoint = format!("{base}/api/desktop/events");
            let agent = ureq::AgentBuilder::new()
                .timeout_read(Duration::from_secs(120))
                .build();
            let mut req = agent.get(&endpoint);
            if let Some(tok) = &token {
                req = req.set("Authorization", &format!("Bearer {tok}"));
            }
            match req.call() {
                Ok(resp) => {
                    let reader = BufReader::new(resp.into_reader());
                    for line in reader.lines() {
                        let line = match line {
                            Ok(l) => l,
                            Err(_) => break,
                        };
                        let Some(data) = line.strip_prefix("data:") else {
                            continue;
                        };
                        let data = data.trim();
                        if data.is_empty() {
                            continue;
                        }
                        if let Ok(evt) = serde_json::from_str::<serde_json::Value>(data) {
                            if evt.get("type").and_then(|t| t.as_str()) == Some("task_completed")
                            {
                                let title = evt
                                    .get("title")
                                    .and_then(|t| t.as_str())
                                    .unwrap_or("任务")
                                    .to_string();
                                let session = evt
                                    .get("session_id")
                                    .and_then(|s| s.as_str())
                                    .unwrap_or("")
                                    .to_string();
                                notify_task_completed(&app, &title, &session);
                            }
                        }
                    }
                }
                Err(e) => {
                    eprintln!("godex-desktop: event bridge error: {e}");
                }
            }
            // Reconnect after a short backoff regardless of outcome.
            std::thread::sleep(Duration::from_secs(3));
        }
    });
}

fn notify_task_completed(app: &tauri::AppHandle, title: &str, session: &str) {
    let mut body = format!("任务「{title}」已完成");
    if !session.is_empty() {
        body.push_str(&format!("\n{session}"));
    }
    let _ = app
        .notification()
        .builder()
        .title("godex")
        .body(body)
        .show();
}
