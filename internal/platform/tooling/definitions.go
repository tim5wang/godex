package tooling

import "github.com/tim5wang/godex/internal/contracts/protocol"

type Definition struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

func (d Definition) Schema() map[string]interface{} {
	return map[string]interface{}{
		"name":         d.Name,
		"description":  d.Description,
		"input_schema": cloneMap(d.InputSchema),
	}
}

func (d Definition) ToolSchema() protocol.ToolSchema {
	return protocol.ToolSchema{
		Name:        d.Name,
		Description: d.Description,
		InputSchema: cloneMap(d.InputSchema),
	}
}

func BashDefinition() Definition {
	return Definition{
		Name:        "bash",
		Description: "Run a shell command from the workspace root. Sandbox limits: no command substitution $() or backticks (precompute values instead); heredocs are supported but avoid embedding unbalanced quotes inside them; file writes are restricted to the workspace (use .godex/tmp for scratch scripts). Prefer one compound command (cmd1 && cmd2) over several sequential calls when the steps are dependent.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]string{"type": "string"},
			},
			"required": []string{"command"},
		},
	}
}

func ReadFileDefinition() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read UTF-8 text file contents from a workspace-relative path, or from an exact read-only attachment path supplied in the current conversation. Returns content with line numbers. Source code files are returned in full; other files default to 2000 lines. Do not use for binary files such as PDFs, images, media, or archives — use attach_file instead.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string", "description": "Workspace-relative path such as agent/agent.go, or the exact read-only path shown for a current-session attachment"},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "1-based line number to start reading from. Default: 1.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of lines to return. Source code files default to all lines; other files default to 2000.",
				},
				"include_line_numbers": map[string]interface{}{
					"type":        "boolean",
					"description": "Include line numbers in output. Default: true.",
				},
			},
			"required": []string{"path"},
		},
	}
}

func WriteFileDefinition() Definition {
	return Definition{
		Name:        "write_file",
		Description: "Write content to a workspace-relative path",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string", "description": "Workspace-relative path such as notes/todo.txt"},
				"content": map[string]string{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
	}
}

func EditFileDefinition() Definition {
	return Definition{
		Name:        "edit_file",
		Description: "Make precise text replacements in workspace-relative files. Three modes, tried in order: (1) files[] — batch edits across MULTIPLE files in ONE call (up to 20 files); all files are validated first and if any old_text fails to match, NOTHING is written to ANY file, making it the safest way to do coordinated cross-file changes in a single round-trip. (2) path + edits[] — multiple independent changes to the same file in one call (up to 50 edits), applied atomically. (3) path + old_text/new_text — single replacement. Prefer batching: many sequential edit_file calls waste round-trips. Every old_text must appear exactly once in its file and must match the ORIGINAL file content verbatim (whitespace included) — re-read the region first if you have not read it in this session. On a failed multi-line old_text, the error pinpoints the first diverging line.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string", "description": "Workspace-relative path such as skill/skill.go. Required for edits[] and old_text/new_text modes; omit when using files[]."},
				"old_text": map[string]interface{}{
					"type":        "string",
					"description": "Exact text to find and replace. Must be unique in the file. Use with new_text for single edit.",
				},
				"new_text": map[string]interface{}{
					"type":        "string",
					"description": "Replacement text. Omit or use empty string to delete old_text.",
				},
				"edits": map[string]interface{}{
					"type":        "array",
					"description": "Multiple non-overlapping edits to the file at path. Every old_text must match the ORIGINAL file (before any edits are applied). Edits must not overlap and each old_text must appear exactly once.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"old_text": map[string]string{"type": "string"},
							"new_text": map[string]string{"type": "string"},
						},
						"required": []string{"old_text", "new_text"},
					},
				},
				"files": map[string]interface{}{
					"type":        "array",
					"description": "Batch edits across multiple files in one call. Each entry has a path and its own edits[] (same rules as above). All files are validated before any write; one bad old_text aborts the whole batch with no file modified.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path": map[string]string{"type": "string"},
							"edits": map[string]interface{}{
								"type": "array",
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"old_text": map[string]string{"type": "string"},
										"new_text": map[string]string{"type": "string"},
									},
									"required": []string{"old_text", "new_text"},
								},
							},
						},
						"required": []string{"path", "edits"},
					},
				},
			},
		},
	}
}

// FileEdit represents a single find-and-replace operation.
type FileEdit struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func GrepDefinition() Definition {
	return Definition{
		Name:        "grep",
		Description: "Search file contents using a regex pattern. Returns matching lines with file paths and line numbers. Supports glob filtering and case-insensitive search.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Regex pattern to search for, e.g. 'func.*Handler' or 'TODO'.",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File or directory to search in. Defaults to workspace root.",
				},
				"glob": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern to filter files, e.g. '*.go', '*.{ts,tsx}'.",
				},
				"case_insensitive": map[string]interface{}{
					"type":        "boolean",
					"description": "Perform case-insensitive matching. Default: false.",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum matches to return. Default: 100, max: 500.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func FindDefinition() Definition {
	return Definition{
		Name:        "find",
		Description: "Find files matching a glob pattern. Searches recursively through directories. Supports patterns like '*.go', '**/*_test.go', 'cmd/**/main.go'.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern to match files, e.g. '**/*.go' or '*.md'.",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory to search in. Defaults to workspace root.",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum files to return. Default: 200, max: 1000.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func LsDefinition() Definition {
	return Definition{
		Name:        "ls",
		Description: "List the contents of a directory. Returns file names, types (file/directory), and sizes.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory path to list. Defaults to workspace root.",
				},
			},
		},
	}
}

func AttachFileDefinition() Definition {
	return Definition{
		Name:        "attach_file",
		Description: "Attach a local workspace file, or a file at an exact read-only attachment path supplied in the current conversation, to the session reply without reading its contents. Use for screenshots, PDFs, downloads, and other files that should be sent as attachments.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Workspace-relative path such as .godex/.tmp/report.pdf, or the exact read-only path shown for a current-session attachment",
				},
			},
			"required": []string{"path"},
		},
	}
}

func SupportedToolSchemas(names ...string) []protocol.ToolSchema {
	result := make([]protocol.ToolSchema, 0, len(names))
	for _, name := range names {
		switch name {
		case "bash":
			result = append(result, BashDefinition().ToolSchema())
		case "read_file":
			result = append(result, ReadFileDefinition().ToolSchema())
		case "write_file":
			result = append(result, WriteFileDefinition().ToolSchema())
		case "edit_file":
			result = append(result, EditFileDefinition().ToolSchema())
		case "attach_file":
			result = append(result, AttachFileDefinition().ToolSchema())
		case "grep":
			result = append(result, GrepDefinition().ToolSchema())
		case "find":
			result = append(result, FindDefinition().ToolSchema())
		case "ls":
			result = append(result, LsDefinition().ToolSchema())
		}
	}
	return result
}
