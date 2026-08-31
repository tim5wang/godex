package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestStaticRoutePatternsHaveSingleOwner(t *testing.T) {
	owners := staticRouteOwners(t)
	var duplicates []string
	for pattern, locations := range owners {
		if len(locations) > 1 {
			duplicates = append(duplicates, pattern+": "+strings.Join(locations, ", "))
		}
	}
	sort.Strings(duplicates)
	if len(duplicates) > 0 {
		t.Fatalf("static route patterns must have one owner:\n%s", strings.Join(duplicates, "\n"))
	}

	expectedOwners := map[string][]string{
		"routes_config.go": {
			"GET /config/meta", "GET /config/schema", "GET /config", "PUT /config",
			"POST /config/reload", "POST /config/reveal", "GET /config/doctor",
		},
		"routes_control.go": {
			"GET /runtime/service", "POST /runtime/service/restart",
			"GET /control/nodes", "GET /control/nodes/{id}", "DELETE /control/nodes/{id}",
			"POST /control/nodes/register", "POST /control/nodes/{id}/heartbeat",
			"POST /control/nodes/{id}/credential", "GET /control/nodes/{id}/overview",
		},
		"routes_providers.go": {
			"GET /providers", "POST /providers/{id}/test", "POST /providers/{id}/models",
			"GET /providers/import/codex", "POST /providers/import/codex",
		},
		"routes_automation.go": {
			"GET /automation/cron/jobs", "POST /automation/cron/jobs",
			"GET /automation/cron/jobs/{id}", "PATCH /automation/cron/jobs/{id}",
			"DELETE /automation/cron/jobs/{id}", "POST /automation/cron/jobs/{id}/run",
			"GET /automation/cron/jobs/{id}/runs", "GET /automation/heartbeat",
			"PUT /automation/heartbeat", "POST /automation/heartbeat/test",
			"GET /automation/heartbeat/logs",
		},
		"routes_memory.go": {
			"GET /memory", "GET /memory/candidates", "GET /memory/audit",
			"POST /memory/digest", "POST /memory/mine/project", "GET /memory/suppressions",
			"GET /memory/context", "POST /memory/remember", "POST /memory/update",
			"POST /memory/forget", "POST /memory/archive", "POST /memory/restore",
			"POST /memory/milestones/archive", "GET /memory/milestones",
			"POST /memory/suppressions/remove", "POST /memory/candidates/{fingerprint}/accept",
			"POST /memory/candidates/{fingerprint}/dismiss", "POST /memory/audit/{id}/restore",
		},
		"routes_notes.go": {
			"GET /notes", "GET /notes/{id}", "GET /notes/{id}/related-memories",
			"POST /notes", "DELETE /notes/{id}",
		},
		"routes_channels.go": {
			"GET /channels", "GET /channels/weixin/auth",
			"POST /channels/weixin/auth/start", "POST /channels/weixin/auth/logout",
		},
		"routes_packages.go": {
			"GET /models", "GET /security/summary", "GET /security/audit",
			"GET /packages", "GET /packages/quality", "POST /packages/install",
			"POST /packages/remove", "POST /packages/{name}/reinstall",
			"POST /packages/{name}/smoke/{smoke}", "GET /prompts", "GET /commands",
			"GET /packages/commands", "GET /packages/roles",
		},
		"routes_gateway.go": {
			"POST /v1/chat/completions", "POST /v1/responses", "POST /v1/messages",
			"POST /v1/exec", "GET /v1/models",
		},
		"routes_sessions.go": {
			"POST /sessions", "GET /sessions", "DELETE /sessions/{id}",
			"PATCH /sessions/{id}/title", "POST /sessions/{id}/fork", "POST /sessions/{id}/model",
			"GET /sessions/{id}", "GET /sessions/{id}/context-inspector",
			"GET /sessions/{id}/transcript/{ref}", "GET /sessions/{id}/ledger",
			"POST /sessions/{id}/ledger", "GET /sessions/{id}/timeline",
			"GET /sessions/{id}/timeline/page", "GET /sessions/{id}/compactions",
		},
		"routes_workflows.go": {
			"GET /sessions/{id}/subagents", "GET /sessions/{id}/subagents/{jobID}",
			"GET /sessions/{id}/subagents/{jobID}/review", "POST /sessions/{id}/subagents/{jobID}/cancel",
			"POST /sessions/{id}/subagents/{jobID}/resume", "POST /sessions/{id}/subagents/{jobID}/merge",
			"GET /sessions/{id}/longtasks", "POST /sessions/{id}/longtasks",
			"GET /sessions/{id}/longtasks/{workflowID}", "POST /sessions/{id}/longtasks/{workflowID}/run",
			"POST /sessions/{id}/longtasks/{workflowID}/cancel", "POST /sessions/{id}/longtasks/{workflowID}/finalize",
			"POST /sessions/{id}/longtasks/{workflowID}/lookup", "POST /sessions/{id}/longtasks/{workflowID}/rollback",
			"POST /sessions/{id}/longtasks/{workflowID}/gc", "GET /sessions/{id}/permissions",
			"POST /sessions/{id}/permissions/{requestID}/approve", "POST /sessions/{id}/permissions/{requestID}/deny",
		},
		"routes_templates.go": {
			"GET /agent-templates", "GET /agent-templates/options", "GET /agent-templates/{id}",
			"POST /agent-templates", "PUT /agent-templates/{id}", "DELETE /agent-templates/{id}",
			"POST /agent-templates/{id}/validate",
		},
		"routes_skills.go": {
			"GET /skills/catalog", "GET /sessions/{id}/skills/catalog", "GET /sessions/{id}/skills/sources",
			"GET /sessions/{id}/skills/active", "GET /sessions/{id}/skills/{name}",
			"POST /sessions/{id}/skills/install", "POST /sessions/{id}/skills/normalize",
			"DELETE /sessions/{id}/skills/{name}", "POST /sessions/{id}/skills/load",
			"POST /sessions/{id}/skills/expand", "POST /sessions/{id}/skills/unload",
		},
		"routes_turns.go": {
			"POST /sessions/{id}/messages", "GET /sessions/{id}/turns/{turnID}",
			"POST /sessions/{id}/turns/{turnID}/cancel", "POST /sessions/{id}/queued/{queueID}/cancel",
			"POST /sessions/{id}/queued/{queueID}/steer", "POST /sessions/{id}/turns/{turnID}/retry",
			"POST /sessions/{id}/turns/{turnID}/resume", "POST /sessions/{id}/attachments",
			"GET /sessions/{id}/attachments/{attachmentID}", "POST /sessions/{id}/commands",
			"GET /sessions/{id}/events",
		},
	}
	for file, patterns := range expectedOwners {
		for _, pattern := range patterns {
			locations := owners[pattern]
			if len(locations) != 1 || !strings.HasPrefix(locations[0], file+":") {
				t.Fatalf("route %q must be owned by %s, got %v", pattern, file, locations)
			}
		}
	}
}

func staticRouteOwners(t *testing.T) map[string][]string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve route ownership test path")
	}
	dir := filepath.Dir(currentFile)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("list httpapi source files: %v", err)
	}

	owners := make(map[string][]string)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("decode route pattern in %s: %v", filepath.Base(path), err)
			}
			position := fset.Position(literal.Pos())
			owners[pattern] = append(owners[pattern], filepath.Base(path)+":"+strconv.Itoa(position.Line))
			return true
		})
	}
	return owners
}
