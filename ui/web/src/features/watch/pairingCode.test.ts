import { describe, expect, it } from "vitest";
import { formatPairingCode, isPairingCodeComplete, normalizePairingCode } from "./pairingCode";

describe("normalizePairingCode", () => {
  it("strips the space the watch displays", () => {
    // The watch renders "9903 7225"; a user typing it back must not be rejected.
    expect(normalizePairingCode("9903 7225")).toBe("99037225");
  });

  it("strips every non-digit, not just spaces", () => {
    expect(normalizePairingCode("99-03_7225")).toBe("99037225");
    expect(normalizePairingCode(" 9903\t7225 ")).toBe("99037225");
  });

  it("caps the length so a long paste cannot overflow the field", () => {
    expect(normalizePairingCode("123456789012")).toBe("12345678");
  });

  it("tolerates empty input", () => {
    expect(normalizePairingCode("")).toBe("");
    expect(normalizePairingCode("abc")).toBe("");
  });
});

describe("formatPairingCode", () => {
  it("groups a full code in fours, matching the watch", () => {
    expect(formatPairingCode("99037225")).toBe("9903 7225");
  });

  it("does not insert a separator before four digits have been typed", () => {
    expect(formatPairingCode("990")).toBe("990");
    expect(formatPairingCode("9903")).toBe("9903");
  });

  it("is idempotent on already-formatted input", () => {
    expect(formatPairingCode("9903 7225")).toBe("9903 7225");
  });
});

describe("isPairingCodeComplete", () => {
  it("is true only at the full length", () => {
    expect(isPairingCodeComplete("9903 7225")).toBe(true);
    expect(isPairingCodeComplete("9903 722")).toBe(false);
    expect(isPairingCodeComplete("")).toBe(false);
  });

  it("counts digits after normalisation, so formatting is irrelevant", () => {
    // Any separator the user types must not change completeness.
    expect(isPairingCodeComplete("99 03 72 25")).toBe(true);
    expect(isPairingCodeComplete("99-03-7225")).toBe(true);
  });
});
