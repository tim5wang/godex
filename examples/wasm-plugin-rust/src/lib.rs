//! GoDex WASM plugin example — Rust implementation of the mailbox JSON ABI
//! `godex:plugin@0.1`, with zero external dependencies.
//!
//! ABI contract (see `internal/wasmrt/wasmrt.go`):
//!
//! ```text
//! godex_abi_version()    -> ptr   NUL-terminated ABI version string
//! godex_request_buffer() -> ptr   stable mailbox buffer (>= 64 KiB) the host
//!                                 writes the JSON request into
//! godex_tools_list()     -> ptr   NUL-terminated JSON: {"tools":[ToolDecl...]}
//! godex_prompts_list()   -> ptr   NUL-terminated JSON: {"sections":[...]}
//! godex_policy()         -> ptr   explicit decision for the mailbox request
//! godex_invoke()         -> ptr   NUL-terminated JSON response
//! ```
//!
//! Host module `godex:host` exposes the only host calls available:
//! `godex_log`, `godex_kv_get`, `godex_kv_set`, `godex_workspace_read`.
//! Full WASI filesystem/network/shell/env are intentionally NOT available.
//!
//! Build (requires the `wasm32-wasip1` target):
//! ```bash
//! rustup target add wasm32-wasip1
//! cd examples/wasm-plugin-rust
//! cargo build --release --target wasm32-wasip1
//! cp target/wasm32-wasip1/release/godex_hello_plugin.wasm ../hello-plugin.wasm
//! ```

static mut MAILBOX: [u8; 128 * 1024] = [0; 128 * 1024];
static mut RESPONSE: [u8; 4096] = [0; 4096];
static mut TOOLS: [u8; 4096] = [0; 4096];
static mut PROMPTS: [u8; 4096] = [0; 4096];
static mut ABI: [u8; 64] = [0; 64];
static mut KV_SCRATCH: [u8; 1024] = [0; 1024];

unsafe fn put(buf: &'static mut [u8], value: &str) -> u32 {
    let bytes = value.as_bytes();
    let n = bytes.len().min(buf.len() - 1);
    buf[..n].copy_from_slice(&bytes[..n]);
    buf[n] = 0;
    buf.as_ptr() as u32
}

unsafe fn request() -> &'static str {
    let end = MAILBOX.iter().position(|&b| b == 0).unwrap_or(MAILBOX.len());
    std::str::from_utf8(&MAILBOX[..end]).unwrap_or("")
}

#[no_mangle]
pub extern "C" fn godex_abi_version() -> u32 {
    unsafe { put(&mut ABI, "godex:plugin@0.1") }
}

#[no_mangle]
pub extern "C" fn godex_request_buffer() -> u32 {
    unsafe { MAILBOX.as_ptr() as u32 }
}

#[no_mangle]
pub extern "C" fn godex_tools_list() -> u32 {
    let tools = concat!(
        r#"{"tools":["#,
        r#"{"name":"rust_echo","description":"echo the message back (Rust plugin)","#,
        r#""inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}},"#,
        r#"{"name":"rust_ping","description":"returns pong","inputSchema":{"type":"object"}},"#,
        r#"{"name":"rust_counter","description":"increment a persisted counter via the plugin KV host call","#,
        r#""inputSchema":{"type":"object"}},"#,
        r#"{"name":"rust_http","description":"fetch a URL via the controlled host HTTP call","#,
        r#""inputSchema":{"type":"object"}}]"#,
        r#"}"#,
    );
    unsafe { put(&mut TOOLS, tools) }
}

#[no_mangle]
pub extern "C" fn godex_prompts_list() -> u32 {
    let prompts = r#"{"sections":[{"key":"rust_plugin_note","kind":"background","text":"A Rust WASM plugin is active; rust_echo, rust_ping and rust_counter are available."}]}"#;
    unsafe { put(&mut PROMPTS, prompts) }
}

#[no_mangle]
pub extern "C" fn godex_policy() -> u32 {
    let req = unsafe { request() };
    let decision = if contains(req, "rust_secret") {
        r#"{"action":"deny","error":{"code":"policy_denied","message":"rust_secret is denied by plugin policy"}}"#
    } else {
        r#"{"action":"continue"}"#
    };
    unsafe { put(&mut RESPONSE, decision) }
}

#[no_mangle]
pub extern "C" fn godex_invoke() -> u32 {
    let req = unsafe { request() };
    let response = dispatch(req);
    unsafe { put(&mut RESPONSE, &response) }
}

// ---------------------------------------------------------------------------
// Host imports (阶段 C KV broker: godex_kv_get / godex_kv_set are wired by the
// host to this plugin's own namespaced store)
// ---------------------------------------------------------------------------

#[link(wasm_import_module = "godex:host")]
extern "C" {
    #[allow(dead_code)]
    fn godex_log(ptr: u32, len: u32);
    fn godex_kv_get(key_ptr: u32, key_len: u32, out_ptr: u32, out_len: u32) -> u32;
    fn godex_kv_set(key_ptr: u32, key_len: u32, val_ptr: u32, val_len: u32);
    #[allow(dead_code)]
    fn godex_workspace_read(rel_ptr: u32, rel_len: u32, out_ptr: u32, out_len: u32) -> u32;
    fn godex_http_get(url_ptr: u32, url_len: u32, out_ptr: u32, out_len: u32) -> u32;
}

/// Read the KV value for `key` via the host call. Returns "" when absent.
fn kv_get(key: &str) -> String {
    let key_bytes = key.as_bytes();
    let mut out = [0u8; 1024];
    let written = unsafe {
        godex_kv_get(
            key_bytes.as_ptr() as u32,
            key_bytes.len() as u32,
            out.as_mut_ptr() as u32,
            out.len() as u32,
        )
    };
    if written == 0 {
        return String::new();
    }
    let n = written as usize;
    String::from_utf8_lossy(&out[..n.min(out.len())]).to_string()
}

/// Store `value` for `key` via the host call.
fn kv_set(key: &str, value: &str) {
    let key_bytes = key.as_bytes();
    let val_bytes = value.as_bytes();
    unsafe {
        godex_kv_set(
            key_bytes.as_ptr() as u32,
            key_bytes.len() as u32,
            val_bytes.as_ptr() as u32,
            val_bytes.len() as u32,
        );
    }
}

// ---------------------------------------------------------------------------
// Minimal JSON helpers (no external crates)
// ---------------------------------------------------------------------------

fn json_string_field<'a>(input: &'a str, field: &str) -> Option<&'a str> {
    let needle = format!("\"{}\"", field);
    let idx = input.find(&needle)?;
    let after = &input[idx + needle.len()..];
    let after = after.trim_start();
    let after = after.strip_prefix(':')?.trim_start();
    let after = after.strip_prefix('"')?;
    let end = after.find('"')?;
    Some(&after[..end])
}

fn json_argument_string<'a>(input: &'a str, field: &str) -> Option<&'a str> {
    let needle = format!("\"{}\"", field);
    let args_start = input.find("\"arguments\"")?;
    let after_args = &input[args_start + "\"arguments\"".len()..];
    let after_args = after_args.trim_start().strip_prefix(':')?.trim_start();
    let value_start = after_args.find(&needle)?;
    let value = &after_args[value_start + needle.len()..];
    let value = value.trim_start().strip_prefix(':')?.trim_start();
    let value = value.strip_prefix('"')?;
    let end = value.find('"')?;
    Some(&value[..end])
}

fn contains(haystack: &str, needle: &str) -> bool {
    haystack.contains(needle)
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

fn dispatch(req: &str) -> String {
    let action = json_string_field(req, "action").unwrap_or("");
    match action {
        "tool_call" => match json_string_field(req, "tool").unwrap_or("") {
            "rust_echo" => {
                let message = json_argument_string(req, "message").unwrap_or("");
                format!(r#"{{"ok":true,"result":"rust echo: {}"}}"#, message)
            }
            "rust_ping" => r#"{"ok":true,"result":"pong"}"#.to_string(),
            "rust_http" => {
                let url = json_argument_string(req, "url").unwrap_or("");
                let mut out = [0u8; 2048];
                let status = unsafe {
                    godex_http_get(
                        url.as_ptr() as u32,
                        url.len() as u32,
                        out.as_mut_ptr() as u32,
                        out.len() as u32,
                    )
                };
                if status != 0 {
                    format!(r#"{{"ok":false,"error":"http fetch failed with status {}"}}"#, status)
                } else {
                    let n = out.iter().position(|&b| b == 0).unwrap_or(out.len());
                    let body = String::from_utf8_lossy(&out[..n]).to_string();
                    format!(r#"{{"ok":true,"result":"{}"}}"#, body)
                }
            }
            "rust_counter" => {
                // Read-modify-write through the host KV broker.
                let current: i64 = kv_get("counter").trim().parse().unwrap_or(0);
                let next = current + 1;
                kv_set("counter", &next.to_string());
                format!(r#"{{"ok":true,"result":"counter: {}"}}"#, next)
            }
            other => format!(r#"{{"ok":false,"error":"unknown tool: {}"}}"#, other),
        },
        "ping" => r#"{"ok":true,"result":"pong"}"#.to_string(),
        other => format!(r#"{{"ok":false,"error":"unknown action: {}"}}"#, other),
    }
}
