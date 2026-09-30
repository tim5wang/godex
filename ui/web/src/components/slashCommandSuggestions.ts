import type { CommandMetadata, PackageCommandEntry } from "../lib/types";

export interface SlashCommandCandidates {
  skills?: Array<{ name: string; description?: string; sections?: string[] }>;
  activeSkills?: Array<{ name: string; description?: string; available_sections?: string[] }>;
  mcpServers?: Array<{ name: string; type?: string }>;
}

export interface SlashCommandSuggestion {
  key: string;
  invocation: string;
  description?: string;
  inputHint?: string;
  mode?: string;
  roles?: string[];
  bundles?: string[];
}

export function matchSlashCommands(
  value: string,
  builtinCommands: CommandMetadata[],
  packageCommands: PackageCommandEntry[],
  candidates: SlashCommandCandidates = {},
): SlashCommandSuggestion[] {
  const typed = value.trimStart();
  if (!typed.startsWith("/")) {
    return [];
  }

  const query = typed.slice(1);
  const commandSeparator = query.indexOf(" ");
  const commandName = commandSeparator < 0 ? query : query.slice(0, commandSeparator);
  const command = builtinCommands.find((item) => item.name.toLowerCase() === commandName.toLowerCase());

  if (command && commandSeparator >= 0 && command.subcommands?.length) {
    const remainder = query.slice(commandSeparator + 1).trimStart();
    const subcommandSeparator = remainder.indexOf(" ");
    const subcommandName = subcommandSeparator < 0 ? remainder : remainder.slice(0, subcommandSeparator);
    const subcommand = command.subcommands.find((item) => item.name.toLowerCase() === subcommandName.toLowerCase());

    if (subcommandSeparator < 0 || !subcommand) {
      return command.subcommands
        .filter((item) => matchesQuery(`${item.name} ${item.description}`, subcommandName))
        .slice(0, 8)
        .map((item) => ({
          key: `subcommand:${command.name}:${item.name}`,
          invocation: `/${command.name} ${item.name}`,
          description: item.description,
          inputHint: item.arguments?.map((argument) => argument.hint).filter(Boolean).join(" "),
        }));
    }

    const argumentText = remainder.slice(subcommandSeparator + 1);
    const argumentTokens = argumentText.trim().split(/\s+/).filter(Boolean);
    const hasTrailingSpace = /\s$/.test(argumentText);
    const argumentIndex = hasTrailingSpace ? argumentTokens.length : Math.max(0, argumentTokens.length - 1);
    const argument = subcommand.arguments?.[argumentIndex];
    if (!argument?.candidate_source) {
      return [];
    }

    const argumentQuery = hasTrailingSpace ? "" : (argumentTokens[argumentIndex] ?? "");
    const previousArguments = argumentTokens.slice(0, argumentIndex);
    const options = candidatesFor(argument.candidate_source, candidates, previousArguments);
    return options
      .filter((item) => matchesQuery(`${item.name} ${item.description ?? ""}`, argumentQuery))
      .slice(0, 8)
      .map((item) => ({
        key: `argument:${command.name}:${subcommand.name}:${item.name}`,
        invocation: `/${command.name} ${subcommand.name} ${[...previousArguments, item.name].join(" ")}`,
        description: item.description,
      }));
  }

  const builtins: SlashCommandSuggestion[] = builtinCommands.map((item) => ({
    key: `builtin:${item.name}`,
    invocation: `/${item.name}`,
    description: item.description,
    inputHint: item.input_hint,
  }));
  const packages: SlashCommandSuggestion[] = packageCommands.map((item) => ({
    key: `pkg:${item.package_name}:${item.namespace || ""}:${item.name}:${item.path}`,
    invocation: `/${item.namespace || item.package_name} ${item.name}`,
    description: item.description,
    mode: item.mode,
    roles: item.roles,
    bundles: item.recommended_bundles,
  }));
  const all = [...builtins, ...packages];

  if (commandSeparator < 0 && !commandName) {
    return all.slice(0, 8);
  }
  return all
    .filter((entry) =>
      normalizeCommandQuery(
        [entry.invocation, entry.description, ...(entry.roles ?? []), ...(entry.bundles ?? [])].filter(Boolean).join(" "),
      ).includes(normalizeCommandQuery(query)),
    )
    .slice(0, 8);
}

function candidatesFor(source: string, candidates: SlashCommandCandidates, previousArguments: string[]): Array<{ name: string; description?: string }> {
  switch (source) {
    case "skills":
      return candidates.skills ?? [];
    case "active_skills":
      return candidates.activeSkills ?? [];
    case "mcp_servers":
      return (candidates.mcpServers ?? []).map((server) => ({
        name: server.name,
        description: server.type,
      }));
    case "skill_sections": {
      const skillName = previousArguments[0];
      const skill = candidates.skills?.find((item) => item.name.toLowerCase() === skillName?.toLowerCase());
      return (skill?.sections ?? []).map((name) => ({ name }));
    }
    default:
      return [];
  }
}

function matchesQuery(value: string, query: string) {
  return !query || normalizeCommandQuery(value).includes(normalizeCommandQuery(query));
}

function normalizeCommandQuery(value: string) {
  return value.toLowerCase().replace(/\s+/g, " ").trim();
}
