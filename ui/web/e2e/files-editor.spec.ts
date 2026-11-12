import { test, expect } from "@playwright/test";
import { mockBackend } from "./fixtures";

// Mocked HTTP, real React/CodeMirror/Chromium: no running Go backend required.
test.beforeEach(async ({ page }) => {
  await mockBackend(page);
  await page.route("**/api/git/status?*", (route) => route.fulfill({ json: {
    repo: true, branch: "test", files: [{ path: "Makefile", index: " ", worktree: "M" }],
  } }));
  await page.route("**/api/git/diff?*", (route) => route.fulfill({ json: {
    repo: true, diff: "@@ -1 +1 @@\n-old target\n+ui-web:\n",
  } }));
});

test("cold Files page reads a tree file using asynchronous workspace metadata", async ({ page }) => {
  // A delayed meta response makes the first render's empty workspace explicit.
  await page.route("**/api/meta", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 150));
    await route.fulfill({ json: { workspace_dir: "/tmp/godex-playwright", auth_required: false } });
  });
  await page.goto("/files");
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content for Playwright");
  await expect(page.getByPlaceholder("Workspace path...")).toHaveValue("/tmp/godex-playwright");
});

test("selecting a tree file after a Git change returns to the editor, including the same file", async ({ page }) => {
  await page.goto("/files");
  // Set an explicit workspace to isolate view selection from the cold-load bug.
  await page.getByPlaceholder("Workspace path...").fill("/tmp/explicit-workspace");
  await page.getByRole("button", { name: "Switch", exact: true }).click();
  await expect(page.getByPlaceholder("Workspace path...")).toHaveValue("/tmp/explicit-workspace");
  await page.getByTestId("files-page-sidebar-mode").getByText(/Changes|变更/).click();
  await page.getByTestId("files-git-changes").getByText("Makefile", { exact: true }).click();
  await expect(page.getByTestId("files-git-diff")).toBeVisible();
  await page.getByTestId("files-page-sidebar-mode").getByText(/File tree|文件树/).click();
  await page.locator(".ant-tree-title").getByText("Makefile", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("pnpm --dir ui/web run dev");
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content for Playwright");
  await expect(page.getByPlaceholder("Workspace path...")).toHaveValue("/tmp/explicit-workspace");
});

test("tree reselection and cancelled file switches preserve unsaved edits", async ({ page }) => {
  await page.goto("/files");
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content");
  await page.locator(".cm-content").press("ControlOrMeta+End");
  await page.keyboard.type("UNSAVED");
  await expect(page.getByText("Unsaved", { exact: true })).toBeVisible();
  // Clicking the already selected node can deselect it in DirectoryTree;
  // the second click selects it again. Neither may reset isDirty.
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("UNSAVED");
  await expect(page.getByText("Unsaved", { exact: true })).toBeVisible();
  await page.locator(".ant-tree-title").getByText("Makefile", { exact: true }).click();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("UNSAVED");
});

test("chat Files dock shares Makefile highlighting and indentation folding", async ({ page }) => {
  await page.goto("/chat");
  const panel = page.locator(".chat-v2-right").first();
  if (await panel.getAttribute("data-active-tab") !== "files" || await panel.getAttribute("data-collapsed") !== "false") {
    await page.locator('.chat-v2-topbar-tab[aria-label="Files"]').click();
  }
  await page.getByTestId("files-panel-tree").getByText("Makefile", { exact: true }).click();
  await expect(panel.locator(".cm-content")).toContainText("pnpm --dir ui/web run dev");
  await expect(panel.getByTestId("code-editor-language")).toHaveAttribute("data-language", "makefile");
  await panel.getByTestId("code-editor-fold-all").click();
  await expect(panel.locator(".cm-foldPlaceholder")).toHaveCount(1);
  await panel.getByTestId("code-editor-unfold-all").click();
  await expect(panel.locator(".cm-foldPlaceholder")).toHaveCount(0);
});

test("Files restores the selected file and unsaved buffer after leaving the app", async ({ page }) => {
  await page.goto("/files");
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content");
  await page.locator(".cm-content").press("ControlOrMeta+End");
  await page.keyboard.type("NAVIGATION DRAFT");
  await page.getByRole("menuitem", { name: /Notes/ }).click();
  await expect(page).toHaveURL(/\/notes/);
  await page.getByRole("menuitem", { name: /Files/ }).click();
  await expect(page.locator(".cm-content")).toContainText("NAVIGATION DRAFT");
  await expect(page.getByText("Unsaved", { exact: true })).toBeVisible();
  await expect(page.locator('.ant-tree-node-selected')).toContainText("SPEC.md");
});

test("each workspace restores its own selected file and explicit root across navigation", async ({ page }) => {
  await page.goto("/files");
  await page.locator(".ant-tree-title").getByText("SPEC.md", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content");
  await page.getByPlaceholder("Workspace path...").fill("/tmp/project-b");
  await page.getByRole("button", { name: "Switch", exact: true }).click();
  await expect(page.locator(".cm-content")).toHaveCount(0);
  await page.locator(".ant-tree-title").getByText("Makefile", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("pnpm --dir ui/web run dev");
  await page.getByRole("menuitem", { name: /Notes/ }).click();
  await page.getByRole("menuitem", { name: /Files/ }).click();
  await expect(page.getByPlaceholder("Workspace path...")).toHaveValue("/tmp/project-b");
  await expect(page.locator(".cm-content")).toContainText("pnpm --dir ui/web run dev");
  await page.getByPlaceholder("Workspace path...").fill("/tmp/godex-playwright");
  await page.getByRole("button", { name: "Switch", exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content");
  await expect(page.locator('.ant-tree-node-selected')).toContainText("SPEC.md");
});

test("returning to Files expands the restored file's ancestor folders", async ({ page }) => {
  await page.route("**/api/files/list?*", (route) => {
    const dir = new URL(route.request().url()).searchParams.get("dir");
    return route.fulfill({ json: { items: dir === "."
      ? [{ name: "src", path: "src", isDir: true }]
      : [{ name: "nested.go", path: "src/nested.go", isDir: false }],
    } });
  });
  await page.goto("/files");
  await page.locator(".ant-tree-title").getByText("src", { exact: true }).click();
  await page.locator(".ant-tree-title").getByText("nested.go", { exact: true }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content");
  await page.getByRole("menuitem", { name: /Notes/ }).click();
  await page.getByRole("menuitem", { name: /Files/ }).click();
  await expect(page.locator(".cm-content")).toContainText("Mock SPEC content");
  await expect(page.locator('.ant-tree-node-selected')).toContainText("nested.go");
});

test("Files restores Git view, baseline and changes sidebar after navigation", async ({ page }) => {
  await page.goto("/files");
  await page.getByTestId("files-page-sidebar-mode").getByText(/Changes|变更/).click();
  await page.getByTestId("files-git-changes").getByText("Makefile", { exact: true }).click();
  await expect(page.getByTestId("files-git-diff")).toBeVisible();
  await page.getByTestId("files-git-diff-base").selectOption("worktree");
  await page.getByRole("menuitem", { name: /Notes/ }).click();
  await page.getByRole("menuitem", { name: /Files/ }).click();
  await expect(page.getByTestId("files-git-diff")).toBeVisible();
  await expect(page.getByTestId("files-git-diff-base")).toHaveValue("worktree");
  await expect(page.getByTestId("files-git-changes")).toBeVisible();
});

test("chat Files dock restores its file and unsaved buffer after leaving Chat", async ({ page }) => {
  await page.goto("/chat");
  const panel = page.locator(".chat-v2-right").first();
  const openFiles = async () => {
    if (await panel.getAttribute("data-active-tab") !== "files" || await panel.getAttribute("data-collapsed") !== "false") {
      await page.locator('.chat-v2-topbar-tab[aria-label="Files"]').click();
    }
  };
  await openFiles();
  await page.getByTestId("files-panel-tree").getByText("Makefile", { exact: true }).click();
  await expect(panel.locator(".cm-content")).toContainText("pnpm --dir ui/web run dev");
  await panel.locator(".cm-content").press("ControlOrMeta+End");
  await page.keyboard.type("DOCK DRAFT");
  await page.getByRole("menuitem", { name: /Notes/ }).click();
  await page.getByRole("menuitem", { name: /Chat/ }).click();
  await openFiles();
  await expect(panel.getByTestId("files-panel-preview-path")).toContainText("Makefile");
  await expect(panel.locator(".cm-content")).toContainText("DOCK DRAFT");
});

for (const [path, language, content, canFold] of [
  ["go.mod", "gomod", "module example.com/test\n\nrequire (\n\texample.com/lib v1.0.0 // indirect\n)\n", true],
  [".env", "shell", '# settings\nexport PORT=8080\nNAME="test"\n', false],
  ["Makefile", "makefile", '# build\nall:\n\techo "$(TARGET)"\n', true],
  ["custom.unknown", "text", "group\n  first\n  second\n", true],
] as const) {
  test(`${path} has its expected language and structural folding`, async ({ page }) => {
    await page.route("**/api/files/list?*", (route) => route.fulfill({ json: {
      items: [{ name: path, path, isDir: false, size: content.length }],
    } }));
    await page.route("**/api/files/read?*", (route) => route.fulfill({ json: {
      path, content, size: content.length,
    } }));
    await page.goto("/files");
    await page.locator(".ant-tree-title").getByText(path, { exact: true }).click();
    await expect(page.locator(".cm-content")).toContainText(content.trim().replace(/\n/g, ""));
    await expect(page.getByTestId("code-editor-language")).toHaveAttribute("data-language", language);
    if (language !== "text") {
      // Real editor token spans, not just testing a filename-to-language table.
      await expect(page.locator(".cm-content span[class]").first()).toBeVisible();
    }
    await page.getByTestId("code-editor-fold-all").click();
    await expect(page.locator(".cm-foldPlaceholder")).toHaveCount(canFold ? 1 : 0);
    await page.getByTestId("code-editor-unfold-all").click();
    await expect(page.locator(".cm-foldPlaceholder")).toHaveCount(0);
    if (language === "text") {
      // Unknown formats can be assigned a mode without changing edited text.
      await page.locator(".cm-content").press("ControlOrMeta+End");
      await page.keyboard.type("new unsaved text");
      await page.getByRole("combobox", { name: "Editor language" }).click();
      await page.locator(".ant-select-dropdown").getByText("shell", { exact: true }).click();
      await expect(page.getByTestId("code-editor-language")).toHaveAttribute("data-language", "shell");
      await expect(page.locator(".cm-content")).toContainText(content.trim().replace(/\n/g, ""));
      await expect(page.locator(".cm-content")).toContainText("new unsaved text");
    }
  });
}
