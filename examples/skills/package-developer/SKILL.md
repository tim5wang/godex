---
name: package-developer
description: Create, test, install, reinstall, and remove Godex packages with manifest, command, role, prompt, smoke, and permission diagnostics.
when_to_use:
  - when the user wants to create a Godex package
  - when the user asks how to test, install, reinstall, or uninstall a package
  - when package commands, roles, prompts, smoke tests, permissions, or tool policies need diagnosis
recommended_bundles:
  - packages
  - core_code
  - planning
sections:
  - core
  - create
  - test
  - install
  - debug
---

## Core

Use this skill for Godex package authoring and lifecycle work. A Godex package is declaration-only: install reads `godex.package.yaml` and copies resources, but it must not execute third-party code automatically.

Default workflow:

1. Inspect or create the package directory.
2. Validate `godex.package.yaml` resources point to existing files.
3. Check commands, roles, prompts, permissions, capabilities, tool policy, and smoke declarations.
4. Install from a local path or GitHub source only after explaining what will be installed.
5. Run smoke tests only when the user explicitly asks; expect shell/file approvals when policy requires them.
6. Use reinstall when validating source updates, and remove when cleaning up.

Prefer Web Skills for visual inspection, and package tools/API for exact automation.

## Create

Minimal package layout:

```text
my-package/
├── godex.package.yaml
├── prompts/
│   └── review.md
├── commands/
│   └── review.yaml
├── roles/
│   └── reviewer.yaml
└── docs/
    └── usage.md
```

Minimal `godex.package.yaml`:

```yaml
name: my-package
version: 0.1.0
description: Example Godex package
recommended_bundles:
  - planning
  - subagent
capabilities:
  - review
tool_policy:
  - file:read:workspace
resources:
  prompts:
    - prompts/review.md
  commands:
    - commands/review.yaml
  roles:
    - roles/reviewer.yaml
  docs:
    - docs/usage.md
smoke_tests:
  - name: quick
    command: "test -f godex.package.yaml"
    working_dir: "."
    timeout_seconds: 10
    expected_exit_code: 0
```

Command declaration pattern:

```yaml
name: review
namespace: my-package
description: Review the current project
mode: subagent_job
prompt_path: prompts/review.md
roles:
  - reviewer
write_scope:
  - docs/**
recommended_bundles:
  - planning
  - subagent
```

Role declaration pattern:

```yaml
id: reviewer
name: Reviewer
description: Reviews a workspace and reports risks
default_bundles:
  - core_code
  - planning
tools:
  - read_file
  - glob
write_enabled: false
capabilities:
  - architecture_review
tool_policy:
  - file:read:workspace
budget_hint: "small"
```

Keep prompts concise. Put long background material in `docs/` and reference it from prompts instead of embedding everything in command declarations.

## Test

Before installing:

1. Read `godex.package.yaml`.
2. Confirm every `resources.*` path exists.
3. Confirm command `prompt_path` exists.
4. Confirm command `roles` match files in `roles/`.
5. Confirm smoke `working_dir` stays inside the package.
6. Confirm permissions/tool policy are explicit enough for the expected tools.

After installing:

- Use Web `Skills -> Packages` to inspect package health.
- Use Web quality diagnostics for unknown bundles, missing resources, risky permissions, and smoke quick checks.
- Use `list_packages`, `list_package_commands`, `list_package_roles`, and `list_prompts` for structured inspection.
- Run smoke only after explicit user approval. Smoke uses the normal backend session, shell permission, approval, and security audit path.

## Install

Install sources can be local paths, Git URLs, or GitHub `owner/repo` shorthand.

Local path:

```text
install_package {"source": "/absolute/path/to/my-package"}
```

GitHub shorthand:

```text
install_package {"source": "owner/repo"}
```

Git URL:

```text
install_package {"source": "https://github.com/owner/repo.git"}
```

Web path:

1. Open `Skills`.
2. Open `Packages`.
3. Paste the local path or GitHub source into `Install package`.
4. Inspect `Packages`, `Commands`, `Roles`, and `Prompts`.
5. Run a smoke test manually if needed.

Reinstall after source changes:

```text
reinstall package from Web Skills, or call the package reinstall API for the installed package name.
```

Remove:

```text
remove_package {"name": "my-package"}
```

## Debug

Common checks:

- Package not visible: confirm `godex.package.yaml` exists at repo root or selected local path root.
- Command not visible: confirm `resources.commands` includes the command YAML and command `name` is set.
- Slash command ambiguous: set `namespace` and prefer invoking `/namespace:name`.
- Role not visible: confirm `resources.roles` includes the role YAML and role `id` is set.
- Prompt not loaded: confirm `prompt_path` is relative to the package root and listed command file can find it.
- Smoke pending approval: inspect the pending permission, explain command and working directory, then let the user approve or deny.
- Smoke fails path checks: keep `working_dir` inside the package and avoid symlink escapes.
- Agent cannot use a tool: check package `tool_policy`, role `tools`, active bundles, and global safety profile.
- Install looks stale: use reinstall, then inspect package digest, installed path, commands, roles, prompts, and last smoke run.
