import { describe, expect, it } from "vitest";
import { buildJoinCommand, quoteForShell } from "./joinCommand";

describe("buildJoinCommand", () => {
  it("emits the join command with defaults", () => {
    const cmd = buildJoinCommand({
      centerURL: "https://godex.claw.carc.top",
      nodeID: "my-laptop",
      credential: "ck_a1b2c3",
    });
    expect(cmd).toBe(
      "godex node join 'https://godex.claw.carc.top' --id my-laptop --credential ck_a1b2c3 --trust trusted",
    );
  });

  it("includes trust and name when provided", () => {
    const cmd = buildJoinCommand({
      centerURL: "https://godex.claw.carc.top",
      nodeID: "my-laptop",
      credential: "ck_a1b2c3",
      trustLevel: "guarded-remote",
      name: "dev box",
    });
    expect(cmd).toBe(
      "godex node join 'https://godex.claw.carc.top' --id my-laptop --credential ck_a1b2c3 --trust guarded-remote --name 'dev box'",
    );
  });

  it("quotes a center URL with a path", () => {
    const cmd = buildJoinCommand({
      centerURL: "https://hub.example.com/base",
      nodeID: "n1",
      credential: "ck_x",
    });
    expect(cmd).toContain("join 'https://hub.example.com/base'");
  });

  it("quotes a name containing a single quote safely", () => {
    const cmd = buildJoinCommand({
      centerURL: "https://godex.claw.carc.top",
      nodeID: "n1",
      credential: "ck_x",
      name: "it's mine",
    });
    expect(cmd).toContain("--name 'it'\\''s mine'");
  });

  it("rejects missing required fields", () => {
    expect(() =>
      buildJoinCommand({ centerURL: "", nodeID: "n1", credential: "ck_x" }),
    ).toThrow();
    expect(() =>
      buildJoinCommand({ centerURL: "https://x", nodeID: "", credential: "ck_x" }),
    ).toThrow();
    expect(() =>
      buildJoinCommand({ centerURL: "https://x", nodeID: "n1", credential: "" }),
    ).toThrow();
  });
});

describe("quoteForShell", () => {
  it("wraps plain values in single quotes", () => {
    expect(quoteForShell("abc")).toBe("'abc'");
  });
  it("escapes embedded single quotes", () => {
    expect(quoteForShell("a'b")).toBe("'a'\\''b'");
  });
});
