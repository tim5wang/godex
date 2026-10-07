import { describe, expect, it } from "vitest";
import type { CommandMetadata, PackageCommandEntry } from "../lib/types";
import { matchSlashCommands } from "./slashCommandSuggestions";

const builtinCommands: CommandMetadata[] = [
  {
    name: "skills",
    description: "manage skills",
    subcommands: [
      { name: "list", description: "list available skills" },
      { name: "load", description: "load a skill", arguments: [{ hint: "<skill>", candidate_source: "skills" }] },
      { name: "expand", description: "expand skill sections", arguments: [{ hint: "<skill>", candidate_source: "skills" }, { hint: "<section>", candidate_source: "skill_sections" }] },
      { name: "unload", description: "unload a skill", arguments: [{ hint: "<skill>", candidate_source: "active_skills" }] },
    ],
  },
  {
    name: "mcp",
    description: "manage MCP servers",
    subcommands: [
      { name: "tools", description: "inspect server tools", arguments: [{ hint: "<server>", candidate_source: "mcp_servers" }] },
      { name: "load", description: "load server tools", arguments: [{ hint: "<server>", candidate_source: "mcp_servers" }] },
    ],
  },
  { name: "clear", description: "clear current prompt state" },
  { name: "compact", description: "compact current session" },
  { name: "new", description: "create a new session" },
  { name: "resume", description: "resume a session", input_hint: "[session-id|session-name]" },
];

const packageCommands: PackageCommandEntry[] = [
  { package_name: "browser-tools", name: "search", path: "commands/search.md" },
];
const candidates = {
  skills: [
    { name: "playwright", description: "Browser automation", sections: ["browser", "testing"] },
    { name: "python", description: "Python development" },
  ],
  activeSkills: [{ name: "playwright", description: "Browser automation" }],
  mcpServers: [
    { name: "crm", type: "stdio" },
    { name: "web-search", type: "streamable-http" },
  ],
};

describe("matchSlashCommands", () => {
  it("shows nested subcommands and filters them as the user types", () => {
    expect(matchSlashCommands("/skills ", builtinCommands, packageCommands).map((item) => item.invocation)).toEqual([
      "/skills list",
      "/skills load",
      "/skills expand",
      "/skills unload",
    ]);
    expect(matchSlashCommands("/mcp ", builtinCommands, packageCommands).map((item) => item.invocation)).toEqual([
      "/mcp tools",
      "/mcp load",
    ]);
    expect(matchSlashCommands("/skills lo", builtinCommands, packageCommands).map((item) => item.invocation)).toEqual([
      "/skills load",
      "/skills unload",
    ]);
  });

  it("closes an exact root-command suggestion after its invocation is completed", () => {
    for (const command of ["clear", "compact", "new", "resume"]) {
      expect(matchSlashCommands(`/${command} `, builtinCommands, packageCommands)).toEqual([]);
    }
    expect(matchSlashCommands("/browser-tools search ", builtinCommands, packageCommands)).toEqual([]);
  });

  it("suggests available skill names for skill arguments", () => {
    expect(matchSlashCommands("/skills load ", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/skills load playwright",
      "/skills load python",
    ]);
    expect(matchSlashCommands("/skills load py", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/skills load python",
    ]);
    expect(matchSlashCommands("/skills unload ", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/skills unload playwright",
    ]);
  });

  it("suggests skill sections after selecting a skill for expand", () => {
    expect(matchSlashCommands("/skills expand ", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/skills expand playwright",
      "/skills expand python",
    ]);
    expect(matchSlashCommands("/skills expand playwright ", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/skills expand playwright browser",
      "/skills expand playwright testing",
    ]);
    expect(matchSlashCommands("/skills expand playwright test", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/skills expand playwright testing",
    ]);
  });

  it("suggests MCP server names and closes after the argument is accepted", () => {
    expect(matchSlashCommands("/mcp tools ", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/mcp tools crm",
      "/mcp tools web-search",
    ]);
    expect(matchSlashCommands("/mcp load web", builtinCommands, packageCommands, candidates).map((item) => item.invocation)).toEqual([
      "/mcp load web-search",
    ]);
    expect(matchSlashCommands("/mcp load crm ", builtinCommands, packageCommands, candidates)).toEqual([]);
  });

  it("keeps command-level filtering and ignores non-slash input", () => {
    expect(matchSlashCommands("/skill", builtinCommands, packageCommands).map((item) => item.invocation)).toContain("/skills");
    expect(matchSlashCommands("hello", builtinCommands, packageCommands)).toEqual([]);
  });
});
