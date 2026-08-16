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
	tools := []toolDecl{{
		Name:        "wasm_echo",
		Description: "echo the message back from wasm",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}},"required":["message"]}`),
	}}
	data, _ := json.Marshal(map[string]any{"tools": tools})
	copy(listBuf, data)
	return ptr(listBuf)
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
		if r.Tool == "wasm_echo" {
			msg, _ := r.Arguments["message"].(string)
			out, _ = json.Marshal(map[string]any{"ok": true, "result": "wasm echo: " + msg})
		} else {
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
