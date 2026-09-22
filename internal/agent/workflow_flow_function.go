package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dop251/goja"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/wasmrt"
)

// ---------------------------------------------------------------------------
// Function (code) node execution (P3)
//
// A function node is a pure compute step: it runs a handler against the
// unified context and returns events/result JSON. JS handlers execute in a
// goja sandbox (no network/fs; only ctx read/write + log + a few utils);
// WASM handlers load a node-library ref through wasmrt (wired in P3b).
//
// The node never starts a subagent job — it runs synchronously in the
// scheduler like decision/branch gateways, writes node.Outputs and is
// finalized through the same handoff machinery as other nodes.
// ---------------------------------------------------------------------------

// workflowFunctionContext builds the unified handler context:
//
//	{ inputs: {...}, outputs: { <completed node id>: {<field>: value} },
//	  node: { id, title } }
//
// This is the "one json/map context" that flows carry (design doc §④): every
// node reads the same ctx and writes back node outputs consumed downstream.
func (a *Agent) workflowFunctionContext(state workflowState, node workflowNode) map[string]any {
	inputs := state.Summary.RunInputs
	if inputs == nil {
		inputs = map[string]any{}
	}
	ctx := map[string]any{
		"node": map[string]any{
			"id":    node.ID,
			"title": node.Title,
		},
		"inputs":  inputs,
		"outputs": map[string]any{},
	}
	outs := ctx["outputs"].(map[string]any)
	for _, n := range state.Nodes {
		if n.ID == node.ID {
			continue // own outputs are not visible to itself
		}
		if len(n.Outputs) > 0 {
			outs[n.ID] = n.Outputs
		}
	}
	return ctx
}

// executeWorkflowFunction runs a function node synchronously (P3). On success
// the result JSON is written to node.Outputs and the node is finalized; on
// failure the node goes to error (RetryPolicy respected via the shared
// retry gate).
//
// P4 streaming: a handler may return an ARRAY of objects instead of one
// object — each element is emitted as a node_emitted event (event sourcing on
// the flow) before the node completes. The final outputs aggregate
// { events, count } and merge the last element's fields so downstream
// predicates still read {{nodes.<id>.outputs.<field>}}.
func (a *Agent) executeWorkflowFunction(ctx context.Context, state *workflowState, node *workflowNode) {
	spec := node.FunctionSpec
	if spec == nil {
		a.failWorkflowFunctionNode(state, node, "function node missing spec")
		return
	}
	handlerCtx := a.workflowFunctionContext(*state, *node)
	start := time.Now()
	raw, err := a.runWorkflowFunction(ctx, spec, handlerCtx)
	latency := time.Since(start)
	if err != nil {
		_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
			"event":   "function_failed",
			"node_id": node.ID,
			"error":   err.Error(),
			"at":      time.Now().UTC(),
		})
		a.failWorkflowFunctionNode(state, node, err.Error())
		return
	}
	// Multi-event output: handler returned an array → stream each element as
	// a node_emitted event, then complete with the aggregated outputs.
	if items, ok := raw.([]any); ok {
		events := make([]map[string]any, 0, len(items))
		for i, item := range items {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			events = append(events, m)
			_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
				"event":   "node_emitted",
				"node_id": node.ID,
				"index":   i,
				"payload": m,
				"at":      time.Now().UTC(),
			})
		}
		outputs := map[string]any{"events": events, "count": len(events)}
		if len(events) > 0 {
			// Merge the last element so {{nodes.<id>.outputs.<field>}} and
			// condition predicates see the latest emitted value.
			for k, v := range events[len(events)-1] {
				outputs[k] = v
			}
		}
		a.completeWorkflowFunction(state, node, outputs, latency)
		return
	}
	if m, ok := raw.(map[string]any); ok {
		a.completeWorkflowFunction(state, node, m, latency)
		return
	}
	a.completeWorkflowFunction(state, node, map[string]any{"result": raw}, latency)
}

// failWorkflowFunctionNode terminates a function node as error.
func (a *Agent) failWorkflowFunctionNode(state *workflowState, node *workflowNode, message string) {
	now := time.Now().UTC()
	node.Status = workflowStatusError
	node.Attempt = nextWorkflowAttempt(*node)
	node.Error = message
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	if handoffErr := a.finalizeWorkflowNodeHandoff(state, node, nil, ""); handoffErr != nil {
		node.Error = handoffErr.Error()
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":   "function_error",
		"node_id": node.ID,
		"error":   message,
		"at":      now,
	})
}

// completeWorkflowFunction records a successful function node execution and
// finalizes the node handoff. The handler result is a JSON object (map) that
// becomes node.Outputs, so downstream {{nodes.<id>.outputs.<field>}} and
// condition predicates read it directly.
func (a *Agent) completeWorkflowFunction(state *workflowState, node *workflowNode, result map[string]any, latency time.Duration) {
	now := time.Now().UTC()
	node.Status = workflowStatusCompleted
	node.Attempt = nextWorkflowAttempt(*node)
	node.Outputs = result
	node.Error = ""
	node.JobID = ""
	node.UpdatedAt = now
	node.FinishedAt = now
	payload, _ := json.Marshal(result)
	node.ResultPreview = previewSubagentResultForModel(string(payload))
	if err := a.finalizeWorkflowNodeHandoff(state, node, nil, string(payload)); err != nil {
		node.Status = workflowStatusError
		node.Error = err.Error()
		return
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":      "function_completed",
		"node_id":    node.ID,
		"latency_ms": latency.Milliseconds(),
		"at":         now,
	})
}

// runWorkflowFunction dispatches to the JS (goja) or WASM (wasmrt) runtime.
// It returns the raw decoded handler result: a map (single output), an array
// of maps (streamed events), or any other JSON value (wrapped by callers).
func (a *Agent) runWorkflowFunction(ctx context.Context, spec *workflowFunctionSpec, handlerCtx map[string]any) (any, error) {
	switch strings.ToLower(strings.TrimSpace(spec.Runtime)) {
	case "js":
		return runJSFunction(ctx, spec, handlerCtx)
	case "wasm":
		return a.runWasmFunction(ctx, spec, handlerCtx)
	default:
		return nil, fmt.Errorf("unknown function runtime %q", spec.Runtime)
	}
}

// workflowFunctionWasmBinary resolves a wasm function node's binary from its
// node-library ref: the ref names an installed package (pkgregistry) whose
// runtime declaration points at a .wasm module. The module bytes are read
// once per call and loaded into a fresh wasmrt plugin (P3 single-shot).
func (a *Agent) workflowFunctionWasmBinary(ref string) ([]byte, error) {
	if a == nil || a.cfg == nil {
		return nil, fmt.Errorf("wasm function node: agent runtime unavailable")
	}
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("wasm function node missing ref")
	}
	packages := pkgregistry.NewManager(a.cfg.StateDir, a.cfg.SkillsDir)
	item, err := packages.Get(ref)
	if err != nil {
		return nil, fmt.Errorf("wasm function node ref %q: %w", ref, err)
	}
	modulePath := packages.RuntimeModulePath(item)
	if modulePath == "" {
		return nil, fmt.Errorf("wasm function node ref %q: package has no wasm runtime module", ref)
	}
	binary, err := os.ReadFile(modulePath)
	if err != nil {
		return nil, fmt.Errorf("wasm function node ref %q: %w", ref, err)
	}
	return binary, nil
}

// runWasmFunction loads the ref'd wasm module, calls the handler tool with
// { ctx, event } arguments, and decodes the JSON result. The handler tool is
// the entry name (spec.Handler, default "handle"); the plugin ABI is
// godex:plugin@0.1 (wasmrt). The result may be an object or an array of
// objects (streamed events, P4).
func (a *Agent) runWasmFunction(ctx context.Context, spec *workflowFunctionSpec, handlerCtx map[string]any) (any, error) {
	binary, err := a.workflowFunctionWasmBinary(spec.Ref)
	if err != nil {
		return nil, err
	}
	handler := strings.TrimSpace(spec.Handler)
	if handler == "" {
		handler = "handle"
	}
	plugin, err := wasmrt.NewPlugin(ctx, wasmrt.Config{
		Binary:   binary,
		PluginID: spec.Ref,
		Host:     wasmrt.HostCallbacks{Log: func(message string) { _ = message }},
	})
	if err != nil {
		return nil, fmt.Errorf("wasm function node: load: %w", err)
	}
	defer plugin.Close(ctx)

	// The handler tool must be declared by the plugin.
	tools, err := plugin.ToolsList(ctx)
	if err != nil {
		return nil, fmt.Errorf("wasm function node: tools list: %w", err)
	}
	found := false
	for _, td := range tools {
		if td.Name == handler {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("wasm function node: handler tool %q not declared by plugin %q", handler, spec.Ref)
	}

	result, err := plugin.CallTool(ctx, handler, map[string]any{
		"ctx":   handlerCtx,
		"event": map[string]any{},
	})
	if err != nil {
		return nil, fmt.Errorf("wasm function node: %w", err)
	}
	// Pass through the raw decoded result: map (single), []any (streamed
	// events), or anything else (wrapped by the caller).
	return result, nil
}

// runJSFunction evaluates the handler source in a goja sandbox and calls
// handle(ctx, event). The return value may be a JSON object (single output),
// an array of objects (streamed events), or any other value (wrapped under
// "result"). A hard timeout interrupts runaway scripts.
func runJSFunction(ctx context.Context, spec *workflowFunctionSpec, handlerCtx map[string]any) (any, error) {
	source := spec.Source
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("js function node missing source")
	}
	// Accept both "export function handle(...)" (ESM style from the docs) and
	// plain "function handle(...)" — goja compiles scripts, so strip exports.
	source = strings.ReplaceAll(source, "export function", "function")
	source = strings.ReplaceAll(source, "export default function", "function")

	handler := strings.TrimSpace(spec.Handler)
	if handler == "" {
		handler = "handle"
	}

	vm := goja.New()
	// Sandbox helpers: console.log -> host log line (no-op sink for now).
	logSink := func(msg string) { _ = msg }
	_ = vm.Set("console", map[string]any{
		"log": func(v ...any) {
			parts := make([]string, 0, len(v))
			for _, x := range v {
				parts = append(parts, fmt.Sprint(x))
			}
			logSink(strings.Join(parts, " "))
		},
	})
	// utils: a tiny JSON helper surface (no network/fs).
	_ = vm.Set("utils", map[string]any{
		"stringify": func(v any) string { b, _ := json.Marshal(v); return string(b) },
		"parse": func(s string) any {
			var v any
			if err := json.Unmarshal([]byte(s), &v); err != nil {
				panic(err)
			}
			return v
		},
	})

	program, err := goja.Compile("", source, false)
	if err != nil {
		return nil, fmt.Errorf("js compile: %w", err)
	}
	if _, err := vm.RunProgram(program); err != nil {
		return nil, fmt.Errorf("js eval: %w", err)
	}
	fnVal := vm.Get(handler)
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil, fmt.Errorf("js function node: handler %q not found (need `function %s(ctx, event)`)", handler, handler)
	}

	// Hard timeout via Interrupt (goja returns *InterruptedError).
	timeout := 10 * time.Second
	done := make(chan struct{})
	var callRes goja.Value
	var callErr error
	go func() {
		defer close(done)
		callRes, callErr = fn(goja.Undefined(), vm.ToValue(handlerCtx), vm.ToValue(map[string]any{}))
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		vm.Interrupt("function timeout")
		<-done
		return nil, fmt.Errorf("js function node: handler timed out after %s", timeout)
	}
	if callErr != nil {
		return nil, fmt.Errorf("js handler: %v", callErr)
	}

	// Decode the result. Objects become the outputs map; ARRAYS are streamed
	// event lists (P4); other values are wrapped under "result".
	switch v := callRes.Export().(type) {
	case map[string]any:
		return v, nil
	case []any:
		return v, nil
	default:
		return map[string]any{"result": callRes.Export()}, nil
	}
}
