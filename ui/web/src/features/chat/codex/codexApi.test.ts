import { describe, expect, it } from "vitest";
import { APIError } from "../../../lib/apiClient";
import { isCodexActiveWriterError } from "./codexApi";

describe("Codex writer conflicts", () => {
  it("recognizes structured conflicts and older node responses", () => {
    expect(isCodexActiveWriterError(new APIError(409, "writer busy", { code: "codex_active_writer" }))).toBe(true);
    expect(isCodexActiveWriterError(new APIError(400, "thread original-thread already has an active writer"))).toBe(true);
  });

  it("does not reinterpret a different conflict, network error, or error body", () => {
    for (const error of [new APIError(409, "active turn changed"), new APIError(403, "thread original-thread already has an active writer"), new APIError(400, "unknown request", null), new Error("thread original-thread already has an active writer")]) {
      expect(isCodexActiveWriterError(error)).toBe(false);
    }
  });
});
