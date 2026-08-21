//go:build wasip1

// GoDex WASM plugin example - TODO/FIXME scanner with cross-session diff.
//
// It demonstrates all four plugin faces of the mailbox JSON ABI
// `godex:plugin@0.1` in one small module:
//
//   - tools:  todo_scan - scan one workspace file for TODO/FIXME/XXX/HACK
//     comment lines (with line numbers)
//   - KV:     persists the last scan per file, so repeat scans report new and
//     resolved TODOs across sessions (plugin KV namespace)
//   - prompt: declares a background prompt section so the agent knows the
//     tool exists
//   - ABI:    godex_abi_version / godex_request_buffer / godex_tools_list /
//     godex_invoke
//
// Host calls used (each gated by a manifest permission):
//
//	godex_workspace_read  <- permissions: [read_file]
//	godex_kv_get/set      <- permissions: [memory]
//
// Build:
//
//	cd examples/wasm-plugin-todo
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
package main

import (
	"encoding/json"
	"strings"
	"unsafe"
)

//go:wasmimport godex:host godex_kv_get
func hostKVGet(keyPtr, keyLen, outPtr, outLen uint32) uint32

//go:wasmimport godex:host godex_kv_set
func hostKVSet(keyPtr, keyLen, valPtr, valLen uint32)

//go:wasmimport godex:host godex_workspace_read
func hostWorkspaceRead(relPtr, relLen, outPtr, outLen uint32) uint32

// Stable linear-memory buffers (the Go WASM heap does not move).
var (
	mailbox   = make([]byte, 128*1024)  // host -> guest request
	abiBuf    = make([]byte, 64)
	listBuf   = make([]byte, 8*1024)
	promptsBuf = make([]byte, 4*1024)
	respBuf   = make([]byte, 256*1024)
	argBuf    = make([]byte, 128*1024)  // host-call argument staging
	valBuf    = make([]byte, 128*1024)  // kv value staging
	outBuf    = make([]byte, 1024*1024) // host-call result buffer (1 MiB)
)

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

// readWorkspaceFile reads one workspace-relative file through the host.
func readWorkspaceFile(rel string) string {
	if len(rel) > len(argBuf) {
		return ""
	}
	copy(argBuf, rel)
	n := hostWorkspaceRead(ptr(argBuf), uint32(len(rel)), ptr(outBuf), uint32(len(outBuf)))
	if n == 0 {
		return ""
	}
	return string(outBuf[:n])
}

func kvGet(key string) string {
	if len(key) > len(argBuf) {
		return ""
	}
	copy(argBuf, key)
	n := hostKVGet(ptr(argBuf), uint32(len(key)), ptr(outBuf), uint32(len(outBuf)))
	if n == 0 {
		return ""
	}
	return string(outBuf[:n])
}

func kvSet(key, value string) {
	if len(key) > len(argBuf) || len(value) > len(valBuf) {
		return
	}
	copy(argBuf, key)
	copy(valBuf, value)
	hostKVSet(ptr(argBuf), uint32(len(key)), ptr(valBuf), uint32(len(value)))
}

type toolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type todoItem struct {
	Line int    `json:"line"`
	Text string `json:"text"`
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
		Name:        "todo_scan",
		Description: "Scan one workspace file for TODO/FIXME/XXX/HACK comment lines with line numbers; diffs against the previous scan (persisted in plugin KV) and reports new/resolved items.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"workspace-relative file path"}},"required":["path"]}`),
	}}
	data, _ := json.Marshal(map[string]any{"tools": tools})
	copy(listBuf, data)
	return ptr(listBuf)
}

//go:wasmexport godex_prompts_list
func godexPromptsList() uint32 {
	sections := []map[string]any{{
		"key":  "todo_tracker",
		"kind": "background",
		"text": "The todo_scan tool is available: given a workspace-relative file path it lists TODO/FIXME/XXX/HACK lines with line numbers and tracks new/resolved items across scans.",
	}}
	data, _ := json.Marshal(map[string]any{"sections": sections})
	copy(promptsBuf, data)
	return ptr(promptsBuf)
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
	switch {
	case r.Action == "ping":
		out = mustJSON(map[string]any{"ok": true, "result": "pong"})
	case r.Action == "tool_call" && r.Tool == "todo_scan":
		out = invokeTodoScan(r.Arguments)
	case r.Action == "tool_call":
		out = mustJSON(map[string]any{"ok": false, "error": "unknown tool: " + r.Tool})
	default:
		out = mustJSON(map[string]any{"ok": false, "error": "unknown action: " + r.Action})
	}
	copy(respBuf, out)
	return ptr(respBuf)
}

func invokeTodoScan(args map[string]any) []byte {
	path, _ := args["path"].(string)
	if strings.TrimSpace(path) == "" {
		return mustJSON(map[string]any{"ok": false, "error": "missing required argument: path"})
	}
	content := readWorkspaceFile(path)
	if content == "" {
		return mustJSON(map[string]any{"ok": false, "error": "cannot read " + path + " (missing, empty, or larger than 1 MiB; needs the read_file permission)"})
	}
	cur := scanTodos(content)
	kvKey := "todos:" + path
	var prev []todoItem
	_ = json.Unmarshal([]byte(kvGet(kvKey)), &prev)
	added, resolved := diffTodos(prev, cur)
	if data, err := json.Marshal(cur); err == nil {
		kvSet(kvKey, string(data))
	}
	return mustJSON(map[string]any{"ok": true, "result": map[string]any{
		"file":     path,
		"total":    len(cur),
		"new":      added,
		"resolved": resolved,
		"todos":    cur,
	}})
}

func scanTodos(content string) []todoItem {
	var items []todoItem
	for i, line := range strings.Split(content, "\n") {
		upper := strings.ToUpper(line)
		if strings.Contains(upper, "TODO") || strings.Contains(upper, "FIXME") ||
			strings.Contains(upper, "XXX") || strings.Contains(upper, "HACK") {
			text := strings.TrimSpace(line)
			if len(text) > 200 {
				text = text[:200]
			}
			items = append(items, todoItem{Line: i + 1, Text: text})
		}
	}
	return items
}

func diffTodos(prev, cur []todoItem) (added, resolved []todoItem) {
	prevSet := make(map[string]bool, len(prev))
	for _, t := range prev {
		prevSet[t.Text] = true
	}
	curSet := make(map[string]bool, len(cur))
	for _, t := range cur {
		curSet[t.Text] = true
	}
	for _, t := range cur {
		if !prevSet[t.Text] {
			added = append(added, t)
		}
	}
	for _, t := range prev {
		if !curSet[t.Text] {
			resolved = append(resolved, t)
		}
	}
	return added, resolved
}

func mustJSON(v any) []byte {
	data, _ := json.Marshal(v)
	return data
}

func main() {}
