import { describe, expect, it } from "vitest";
import { formatBytes, formatCount } from "./byteSize";

describe("formatBytes", () => {
  it("renders zero and absent values as 0 B", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(undefined)).toBe("0 B");
  });

  it("renders bytes without decimals", () => {
    expect(formatBytes(512)).toBe("512 B");
  });

  it("scales to the next unit at 1024", () => {
    expect(formatBytes(1024)).toBe("1.0 KB");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(formatBytes(1024 * 1024)).toBe("1.0 MB");
    expect(formatBytes(3 * 1024 * 1024 * 1024)).toBe("3.0 GB");
  });
});

describe("formatCount", () => {
  it("groups thousands", () => {
    expect(formatCount(1234567)).toBe("1,234,567");
  });

  it("renders zero and absent values as 0", () => {
    expect(formatCount(0)).toBe("0");
    expect(formatCount(undefined)).toBe("0");
  });
});
