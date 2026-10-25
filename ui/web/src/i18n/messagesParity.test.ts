import { describe, expect, it } from "vitest";
import { messages } from "./messages";

// Every message key must exist in BOTH locales.
//
// useI18n falls back to the raw key when a lookup fails, so a missing translation
// renders "memory.types.work_fact" in the UI instead of failing loudly. That is how a
// real gap shipped: `work_fact` / `work_method` were added to the memory types (and to
// the English messages) but never to the Chinese ones, so the Chinese UI showed the
// raw key for two types that exist in the data model.
//
// The whole tree is compared rather than a hand-listed set of keys: the failure mode is
// "someone added a key on one side", and a list only catches keys someone remembered to
// list — the very hole that let this ship.

function leafKeys(locale: "en" | "zh"): string[] {
  const leaves: string[] = [];
  const walk = (node: unknown, prefix: string) => {
    if (typeof node === "string") {
      leaves.push(prefix);
      return;
    }
    if (typeof node !== "object" || node === null) {
      // A non-string, non-object leaf is a structural inconsistency of its own; name
      // it with its type so the diff points at the offending key rather than at "".
      leaves.push(`${prefix}<${typeof node}>`);
      return;
    }
    for (const [key, value] of Object.entries(node as Record<string, unknown>)) {
      walk(value, prefix ? `${prefix}.${key}` : key);
    }
  };
  walk((messages as Record<string, unknown>)[locale], "");
  return leaves;
}

describe("i18n locale parity", () => {
  it("has identical message keys in en and zh", () => {
    const en = new Set(leafKeys("en"));
    const zh = new Set(leafKeys("zh"));
    // Reported as two lists so the missing side is unambiguous in the failure output.
    expect({
      onlyEn: [...en].filter((key) => !zh.has(key)).sort(),
      onlyZh: [...zh].filter((key) => !en.has(key)).sort(),
    }).toEqual({ onlyEn: [], onlyZh: [] });
  });
});
