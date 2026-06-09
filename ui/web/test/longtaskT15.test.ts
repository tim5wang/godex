import { describe, expect, it } from "vitest";

// We exercise the module-level pure helpers from LongTaskRefluxBubble.
// The component itself is JSX and is covered by manual smoke in the
// chat page; the pure helpers are the surface the agent integration
// depends on, so they are the contract for the rest of T15.
import { isLongTaskRefluxMessage } from "../src/features/chat/LongTaskRefluxBubble";
import { ROLLBACK_REASON_MAX_BYTES } from "../src/features/chat/LongTaskRollbackModal";

describe("LongTaskRefluxBubble content sniff", () => {
  it("accepts messages that start with 'LongTask '", () => {
    expect(isLongTaskRefluxMessage("LongTask lt_x: completed\nbody")).toBe(true);
  });

  it("rejects messages that do not start with the marker", () => {
    expect(isLongTaskRefluxMessage("hello world")).toBe(false);
  });

  it("trims leading whitespace before matching", () => {
    expect(isLongTaskRefluxMessage("  LongTask lt_x: blocked")).toBe(true);
  });
});

describe("LongTaskRollbackModal byte cap", () => {
  it("matches the agent / CLI / HTTP 1024-byte cap", () => {
    expect(ROLLBACK_REASON_MAX_BYTES).toBe(1024);
  });
});
