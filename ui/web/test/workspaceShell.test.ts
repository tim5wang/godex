import { createElement, isValidElement } from "react";
import { describe, expect, it } from "vitest";

import { WorkspaceShell, buildWorkspaceShellClassName } from "../src/components/workspace/WorkspaceShell";

describe("WorkspaceShell contract", () => {
  it("builds the stable route shell class name", () => {
    expect(buildWorkspaceShellClassName("chat-shell")).toBe("godex-shell chat-shell");
    expect(buildWorkspaceShellClassName("")).toBe("godex-shell");
    expect(buildWorkspaceShellClassName(undefined)).toBe("godex-shell");
  });

  it("returns the four workspace frame slots as a single shell element", () => {
    const element = WorkspaceShell({
      shellClassName: "godex-shell chat-shell",
      appNav: createElement("nav", { "data-slot": "app-nav" }),
      header: createElement("header", { "data-slot": "header" }),
      content: createElement("main", { "data-slot": "content" }),
      drawer: createElement("aside", { "data-slot": "drawer" }),
    });

    expect(isValidElement(element)).toBe(true);
    expect((element.props as { className?: string }).className).toBe("godex-shell chat-shell");
    expect((element.props as { children?: unknown[] }).children).toHaveLength(3);
  });
});
