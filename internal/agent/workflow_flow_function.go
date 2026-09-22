package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
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
func (a *Agent) executeWorkflowFunction(ctx context.Context, state *workflowState, node *workflowNode) {
	spec := node.FunctionSpec
	if spec == nil {
		a.failWorkflowFunctionNode(state, node, "function node missing spec")
		return
	}
	handlerCtx := a.workflowFunctionContext(*state, *node)
	start := time.Now()
	result, err := runWorkflowFunction(ctx, spec, handlerCtx)
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
	a.completeWorkflowFunction(state, node, result, latency)
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
func runWorkflowFunction(ctx context.Context, spec *workflowFunctionSpec, handlerCtx map[string]any) (map[string]any, error) {
	switch strings.ToLower(strings.TrimSpace(spec.Runtime)) {
	case "js":
		return runJSFunction(ctx, spec, handlerCtx)
	case "wasm":
		return nil, fmt.Errorf("wasm function nodes require the node library (P3b); ref %q not loaded", spec.Ref)
	default:
		return nil, fmt.Errorf("unknown function runtime %q", spec.Runtime)
	}
}

// runJSFunction evaluates the handler source in a goja sandbox and calls
// handle(ctx, event). The return value must be a JSON object; it is decoded
// into the node outputs map. A hard timeout interrupts runaway scripts.
func runJSFunction(ctx context.Context, spec *workflowFunctionSpec, handlerCtx map[string]any) (map[string]any, error) {
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

	// Decode the result. Objects become the outputs map; arrays/values are
	// wrapped under "result" so downstream predicates still work.
	switch v := callRes.Export().(type) {
	case map[string]any:
		return v, nil
	default:
		return map[string]any{"result": callRes.Export()}, nil
	}
}
