package main

import (
	"encoding/json"
	"unsafe"
)

type toolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// global buffers (stable linear memory)
var mailbox = make([]byte, 128*1024)
var abiBuf = make([]byte, 64)
var listBuf = make([]byte, 4096)
var respBuf = make([]byte, 4096)
var promptsBuf = make([]byte, 4096)
var policyBuf = make([]byte, 4096)

func ptr(buf []byte) uint32 { return uint32(uintptr(unsafe.Pointer(&buf[0]))) }

func goString(addr uint32) string {
	if addr == 0 {
		return ""
	}
	p := unsafe.Pointer(uintptr(addr))
	var buf []byte
	for i := 0; i < 128*1024; i++ {
		b := *(*byte)(unsafe.Add(p, i))
		if b == 0 {
			return string(buf)
		}
		buf = append(buf, b)
	}
	return string(buf)
}

//go:wasmexport godex_abi_version
func godexABIVersion() uint32 {
	copy(abiBuf, "godex:plugin@0.1")
	return ptr(abiBuf)
}

//go:wasmexport godex_request_buffer
func godexRequestBuffer() uint32 { return ptr(mailbox) }

//go:wasmexport godex_tools_list
func godexToolsList() uint32 {
	tools := []toolDecl{
		{
			Name:        "wasm_echo",
			Description: "echo the message back from wasm",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
		},
		{
			Name:        "wasm_secret",
			Description: "returns a secret",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
	}
	data, _ := json.Marshal(map[string]any{"tools": tools})
	copy(listBuf, data)
	return ptr(listBuf)
}

//go:wasmexport godex_prompts_list
func godexPromptsList() uint32 {
	sections := []map[string]any{{
		"key":  "wasm_plugin_note",
		"kind": "background",
		"text": "A WASM plugin is active and can echo messages via the wasm_echo tool.",
	}}
	data, _ := json.Marshal(map[string]any{"sections": sections})
	copy(promptsBuf, data)
	return ptr(promptsBuf)
}

//go:wasmexport godex_policy
func godexPolicy() uint32 {
	req := goString(ptr(mailbox))
	var r struct {
		Action string `json:"action"`
		Tool   string `json:"tool"`
	}
	_ = json.Unmarshal([]byte(req), &r)
	var out []byte
	if r.Tool == "wasm_secret" {
		out, _ = json.Marshal(map[string]any{
			"action": "deny",
			"error":  map[string]any{"code": "policy_denied", "message": "wasm_secret is denied by plugin policy"},
		})
	} else {
		out, _ = json.Marshal(map[string]any{"action": "continue"})
	}
	copy(policyBuf, out)
	return ptr(policyBuf)
}

//go:wasmexport godex_invoke
func godexInvoke() uint32 {
	req := goString(ptr(mailbox))
	var r struct {
		Action    string         `json:"action"`
		Tool      string         `json:"tool,omitempty"`
		Arguments map[string]any `json:"arguments,omitempty"`
	}
	_ = json.Unmarshal([]byte(req), &r)
	var out []byte
	switch r.Action {
	case "tool_call":
		switch r.Tool {
		case "wasm_echo":
			msg, _ := r.Arguments["message"].(string)
			out, _ = json.Marshal(map[string]any{"ok": true, "result": "wasm echo: " + msg})
		case "wasm_secret":
			out, _ = json.Marshal(map[string]any{"ok": true, "result": "the secret is 42"})
		default:
			out, _ = json.Marshal(map[string]any{"ok": false, "error": "unknown tool: " + r.Tool})
		}
	case "ping":
		out, _ = json.Marshal(map[string]any{"ok": true, "result": "pong"})
	default:
		out, _ = json.Marshal(map[string]any{"ok": false, "error": "unknown action: " + r.Action})
	}
	copy(respBuf, out)
	return ptr(respBuf)
}

func main() {}
