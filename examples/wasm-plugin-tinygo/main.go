// GoDex WASM plugin example — TinyGo implementation of the mailbox JSON ABI
// `godex:plugin@0.1`.
//
// This example is intentionally ABI-equivalent to the Rust example in
// `examples/wasm-plugin-rust` (same exports, same JSON envelope), but written
// in Go and compiled with TinyGo. TinyGo produces much smaller modules than
// the Go standard toolchain, which is a good fit for constrained plugins.
//
// ABI contract (see `internal/wasmrt/wasmrt.go`):
//
//	godex_abi_version()    -> ptr   NUL-terminated ABI version string
//	godex_request_buffer() -> ptr   stable mailbox buffer (>= 64 KiB)
//	godex_tools_list()     -> ptr   NUL-terminated JSON: {"tools":[ToolDecl...]}
//	godex_prompts_list()   -> ptr   NUL-terminated JSON: {"sections":[...]}
//	godex_policy()         -> ptr   explicit decision for the mailbox request
//	godex_invoke()         -> ptr   NUL-terminated JSON response
//
// Build:
//
//	cd examples/wasm-plugin-tinygo
//	tinygo build -o plugin.wasm -target=wasip1 .
//	cp plugin.wasm ../../internal/wasmrt/testdata/tinygo_plugin.wasm
package main

import "unsafe"

// mailbox is the guest request buffer the host writes into.
var mailbox [128 * 1024]byte

// response/tools/prompts/abi are stable output buffers (never moved by GC in
// TinyGo, so their addresses are valid linear-memory pointers).
var response [4096]byte
var tools [4096]byte
var prompts [4096]byte
var abi [64]byte

// put copies s into buf, NUL-terminates, and returns the buffer pointer.
func put(buf []byte, s string) uint32 {
	n := copy(buf, s)
	if n < len(buf) {
		buf[n] = 0
	}
	return uint32(uintptr(unsafe.Pointer(&buf[0])))
}

// request reads the NUL-terminated JSON request the host wrote into mailbox.
func request() string {
	end := 0
	for end < len(mailbox) && mailbox[end] != 0 {
		end++
	}
	return string(mailbox[:end])
}

//go:wasmexport godex_abi_version
func godexABIVersion() uint32 {
	return put(abi[:], "godex:plugin@0.1")
}

//go:wasmexport godex_request_buffer
func godexRequestBuffer() uint32 {
	return uint32(uintptr(unsafe.Pointer(&mailbox[0])))
}

//go:wasmexport godex_tools_list
func godexToolsList() uint32 {
	return put(tools[:], `{"tools":[{"name":"tiny_echo","description":"echo the message back (TinyGo plugin)","inputSchema":{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}},{"name":"tiny_ping","description":"returns pong","inputSchema":{"type":"object"}}]}`)
}

//go:wasmexport godex_prompts_list
func godexPromptsList() uint32 {
	return put(prompts[:], `{"sections":[{"key":"tinygo_plugin_note","kind":"background","text":"A TinyGo WASM plugin is active; tiny_echo and tiny_ping are available."}]}`)
}

//go:wasmexport godex_policy
func godexPolicy() uint32 {
	req := request()
	if contains(req, "tiny_secret") {
		return put(response[:], `{"action":"deny","error":{"code":"policy_denied","message":"tiny_secret is denied by plugin policy"}}`)
	}
	return put(response[:], `{"action":"continue"}`)
}

//go:wasmexport godex_invoke
func godexInvoke() uint32 {
	return put(response[:], dispatch(request()))
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && index(haystack, needle) >= 0
}

func index(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// jsonField extracts the value of a top-level string field.
func jsonField(input, field string) string {
	needle := `"` + field + `"`
	idx := index(input, needle)
	if idx < 0 {
		return ""
	}
	rest := input[idx+len(needle):]
	rest = trimSpace(rest)
	if len(rest) == 0 || rest[0] != ':' {
		return ""
	}
	rest = trimSpace(rest[1:])
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	end := index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n') {
		s = s[1:]
	}
	return s
}

// dispatch handles one tool_call request.
func dispatch(req string) string {
	action := jsonField(req, "action")
	switch action {
	case "tool_call":
		switch jsonField(req, "tool") {
		case "tiny_echo":
			msg := jsonField(jsonArguments(req), "message")
			return `{"ok":true,"result":"tiny echo: ` + msg + `"}`
		case "tiny_ping":
			return `{"ok":true,"result":"pong"}`
		default:
			return `{"ok":false,"error":"unknown tool"}`
		}
	case "ping":
		return `{"ok":true,"result":"pong"}`
	default:
		return `{"ok":false,"error":"unknown action"}`
	}
}

// jsonArguments returns the substring starting at the "arguments" value so
// jsonField can extract nested fields.
func jsonArguments(input string) string {
	needle := `"arguments"`
	idx := index(input, needle)
	if idx < 0 {
		return ""
	}
	rest := input[idx+len(needle):]
	rest = trimSpace(rest)
	if len(rest) == 0 || rest[0] != ':' {
		return ""
	}
	return rest[1:]
}

func main() {}
