import { describe, expect, it } from "vitest";
import { acpAgentsConfigToForm, acpAgentsFormToConfig } from "./settingsConfigModel";

describe("acpAgentsConfigToForm / acpAgentsFormToConfig args round-trip", () => {
  it("preserves args containing spaces via quoting", () => {
    const config = {
      "dsh-agent": {
        command: "dsh",
        args: ["--profile", "acp", "--prompt", "hello world"],
      },
    };
    // Config -> form: args joined for editing; the spaced arg must be quoted.
    const form = acpAgentsConfigToForm(config);
    const argsText = form.items[0].args;
    expect(argsText).toBe('--profile acp --prompt "hello world"');
    // Form -> config: the quoted arg must come back as one argument.
    const roundTrip = acpAgentsFormToConfig(form);
    expect(roundTrip["dsh-agent"].args).toEqual(["--profile", "acp", "--prompt", "hello world"]);
  });

  it("splits whitespace- and comma-separated args like the legacy form", () => {
    const form = {
      items: [
        {
          id: "a",
          command: "codex",
          args: "codex --profile acp",
          env: "",
          timeout_seconds: 0,
          description: "",
          model: "",
        },
      ],
    };
    expect(acpAgentsFormToConfig(form)["a"].args).toEqual(["codex", "--profile", "acp"]);
    const commaForm = {
      items: [{ id: "a", command: "codex", args: "codex,--profile,acp", env: "", timeout_seconds: 0, description: "", model: "" }],
    };
    expect(acpAgentsFormToConfig(commaForm)["a"].args).toEqual(["codex", "--profile", "acp"]);
  });
});
