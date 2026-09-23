package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Node pre/post bash scripts (E3b)
//
// Every workflow node may declare optional bash scripts that run around its
// main work:
//
//	pre_script  — before the node starts (all kinds, incl. function/decision)
//	post_script — after the node reaches a terminal state (completed only;
//	              errors/cancels skip it)
//
// Scripts execute in the agent's workspace with a hard timeout, and receive
// the run context as environment:
//
//	FLOW_NODE_ID       node id
//	FLOW_NODE_KIND     engine kind (subagent_task / llm_task / function / …)
//	FLOW_INPUTS_JSON   full run inputs as JSON
//	FLOW_INPUT_<NAME>  each input flattened to JSON (uppercase snake name)
//	FLOW_CTX_JSON      path to a temp file with the full unified ctx
//	                    ({ inputs, outputs:{<node>:…}, node:{id,title} })
//
// stdout/stderr are captured onto node.ScriptOutput
// ({pre_stdout,pre_stderr,pre_exit,post_stdout,post_stderr,post_exit}) so
// downstream nodes and the debug panel can read them.
// ---------------------------------------------------------------------------

const (
	scriptDefaultTimeout = 30 * time.Second
	scriptEnvVarCtx      = "FLOW_CTX_JSON"
	scriptEnvVarInputs   = "FLOW_INPUTS_JSON"
)

// runNodeScript executes one bash script in the agent workspace with a hard
// timeout and returns stdout, stderr, exit code and the error (nil when exit
// code is 0). An empty script is a no-op.
func (a *Agent) runNodeScript(ctx context.Context, script string, env map[string]string) (string, string, int, error) {
	script = strings.TrimSpace(script)
	if script == "" {
		return "", "", 0, nil
	}
	wd := ""
	if a != nil && a.cfg != nil {
		wd = a.cfg.WorkspaceDir
	}
	if wd == "" {
		wd = "."
	}
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", script)
	cmd.Dir = wd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		}
	}
	return stdout.String(), stderr.String(), code, err
}

// nodeScriptEnv builds the environment for pre/post scripts from the run
// context. The full unified ctx is written to a temp file (FLOW_CTX_JSON);
// callers should remove it after execution.
func (a *Agent) nodeScriptEnv(state workflowState, node workflowNode) (map[string]string, string, error) {
	env := map[string]string{
		"FLOW_NODE_ID":   node.ID,
		"FLOW_NODE_KIND": node.Kind,
	}
	if len(state.Summary.RunInputs) > 0 {
		if b, err := json.Marshal(state.Summary.RunInputs); err == nil {
			env[scriptEnvVarInputs] = string(b)
		}
		for k, v := range state.Summary.RunInputs {
			if b, err := json.Marshal(v); err == nil {
				env["FLOW_INPUT_"+toEnvName(k)] = string(b)
			}
		}
	}
	ctxJSON := a.workflowFunctionContext(state, node)
	b, err := json.Marshal(ctxJSON)
	if err != nil {
		return env, "", err
	}
	f, err := os.CreateTemp("", "flow_ctx_*.json")
	if err != nil {
		return env, "", err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return env, "", err
	}
	_ = f.Close()
	env[scriptEnvVarCtx] = f.Name()
	return env, f.Name(), nil
}

// runPreScript executes the node's pre_script (if any) before the node's main
// work. On failure the node is moved to error and the error is returned.
func (a *Agent) runPreScript(state *workflowState, node *workflowNode) error {
	if strings.TrimSpace(node.PreScript) == "" {
		return nil
	}
	env, ctxFile, err := a.nodeScriptEnv(*state, *node)
	if ctxFile != "" {
		defer os.Remove(ctxFile)
	}
	if err != nil {
		return fmt.Errorf("pre_script env: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), scriptDefaultTimeout)
	defer cancel()
	stdout, stderr, code, err := a.runNodeScript(ctx, node.PreScript, env)
	if node.ScriptOutput == nil {
		node.ScriptOutput = map[string]any{}
	}
	node.ScriptOutput["pre_stdout"] = stdout
	node.ScriptOutput["pre_stderr"] = stderr
	node.ScriptOutput["pre_exit"] = code
	if err != nil {
		return fmt.Errorf("pre_script failed (exit %d): %s", code, strings.TrimSpace(stderr))
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":   "node_pre_script",
		"node_id": node.ID,
		"exit":    code,
		"at":      time.Now().UTC(),
	})
	return nil
}

// runPostScript executes the node's post_script (if any) after the node
// completed successfully. Failures are recorded (not fatal — the node already
// finished).
func (a *Agent) runPostScript(state *workflowState, node *workflowNode) {
	if strings.TrimSpace(node.PostScript) == "" {
		return
	}
	env, ctxFile, err := a.nodeScriptEnv(*state, *node)
	if ctxFile != "" {
		defer os.Remove(ctxFile)
	}
	if err != nil {
		if node.ScriptOutput == nil {
			node.ScriptOutput = map[string]any{}
		}
		node.ScriptOutput["post_error"] = err.Error()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), scriptDefaultTimeout)
	defer cancel()
	stdout, stderr, code, err := a.runNodeScript(ctx, node.PostScript, env)
	if node.ScriptOutput == nil {
		node.ScriptOutput = map[string]any{}
	}
	node.ScriptOutput["post_stdout"] = stdout
	node.ScriptOutput["post_stderr"] = stderr
	node.ScriptOutput["post_exit"] = code
	if err != nil {
		node.ScriptOutput["post_error"] = err.Error()
	}
	_ = a.workflows.appendEvent(state.Summary.ID, map[string]any{
		"event":   "node_post_script",
		"node_id": node.ID,
		"exit":    code,
		"at":      time.Now().UTC(),
	})
}

// toEnvName upper-snake-cases a variable name for the FLOW_INPUT_* env var
// (e.g. "audioUrl" -> "AUDIO_URL", "task" -> "TASK").
func toEnvName(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(strings.ReplaceAll(b.String(), ".", "_"))
}

var _ = filepath.Join // keep filepath import if unused later
