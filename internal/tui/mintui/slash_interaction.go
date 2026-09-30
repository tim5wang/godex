package mintui

import (
	"strings"

	"github.com/tim5wang/godex/internal/services/commands"
	minitui "github.com/tim5wang/min-tui"
)

type slashArgumentSelector interface {
	Select(prompt string, options []minitui.SelectOption) int
	Prompt(prompt string) string
}

func completeSlashArguments(
	selector slashArgumentSelector,
	command commands.CommandMetadata,
	rawArgs string,
	candidateOptions func(string, []string) ([]minitui.SelectOption, error),
) (string, bool, error) {
	if len(command.Subcommands) == 0 {
		return rawArgs, true, nil
	}

	args := strings.Fields(rawArgs)
	var subcommand *commands.CommandSubcommandMetadata
	if len(args) == 0 {
		options := subcommandOptions(command.Subcommands, "")
		selected := selector.Select("Choose a /"+command.Name+" action", options)
		if selected < 0 || selected >= len(options) {
			return "", false, nil
		}
		subcommand = findSubcommand(command.Subcommands, options[selected].Label)
		args = []string{options[selected].Label}
	} else {
		subcommand = findSubcommand(command.Subcommands, args[0])
		if subcommand == nil {
			options := subcommandOptions(command.Subcommands, args[0])
			if len(options) == 0 {
				return rawArgs, true, nil
			}
			selected := selector.Select("Choose a /"+command.Name+" action", options)
			if selected < 0 || selected >= len(options) {
				return "", false, nil
			}
			args[0] = options[selected].Label
			subcommand = findSubcommand(command.Subcommands, args[0])
		}
	}

	if subcommand == nil {
		return strings.Join(args, " "), true, nil
	}

	for argumentIndex, argument := range subcommand.Arguments {
		commandArgIndex := argumentIndex + 1
		if argument.CandidateSource == "" {
			if !argument.Required {
				break
			}
			if len(args) <= commandArgIndex {
				value := strings.TrimSpace(selector.Prompt("Enter " + argument.Hint))
				if value == "" {
					return "", false, nil
				}
				args = append(args, strings.Fields(value)...)
			}
			continue
		}
		previousArgs := args[1:]
		if len(previousArgs) > argumentIndex {
			previousArgs = previousArgs[:argumentIndex]
		}
		options, err := candidateOptions(argument.CandidateSource, previousArgs)
		if err != nil {
			if len(args) > commandArgIndex {
				return strings.Join(args, " "), true, nil
			}
			return "", false, err
		}
		if len(args) > commandArgIndex && hasExactOption(options, args[commandArgIndex]) {
			continue
		}
		if len(args) > commandArgIndex {
			options = filterOptions(options, args[commandArgIndex])
			if len(options) == 0 {
				return strings.Join(args, " "), true, nil
			}
		}
		if len(options) == 0 {
			return strings.Join(args, " "), true, nil
		}

		prompt := "Choose " + strings.TrimSpace(argument.Hint)
		if argument.Hint == "" {
			prompt = "Choose an argument"
		}
		selected := selector.Select(prompt, options)
		if selected < 0 || selected >= len(options) {
			return "", false, nil
		}
		if len(args) == commandArgIndex {
			args = append(args, options[selected].Label)
		} else {
			args[commandArgIndex] = options[selected].Label
		}
	}
	return strings.Join(args, " "), true, nil
}

func subcommandOptions(items []commands.CommandSubcommandMetadata, query string) []minitui.SelectOption {
	options := make([]minitui.SelectOption, 0, len(items))
	for _, item := range items {
		if query != "" && !strings.HasPrefix(strings.ToLower(item.Name), strings.ToLower(query)) {
			continue
		}
		description := item.Description
		hints := make([]string, 0, len(item.Arguments))
		for _, argument := range item.Arguments {
			if argument.Hint != "" {
				hints = append(hints, argument.Hint)
			}
		}
		if len(hints) > 0 {
			description += " " + strings.Join(hints, " ")
		}
		options = append(options, minitui.SelectOption{Label: item.Name, Description: description})
	}
	return options
}

func findSubcommand(items []commands.CommandSubcommandMetadata, name string) *commands.CommandSubcommandMetadata {
	for i := range items {
		if strings.EqualFold(items[i].Name, name) {
			return &items[i]
		}
	}
	return nil
}

func hasExactOption(options []minitui.SelectOption, query string) bool {
	for _, option := range options {
		if strings.EqualFold(option.Label, query) {
			return true
		}
	}
	return false
}

func filterOptions(options []minitui.SelectOption, query string) []minitui.SelectOption {
	filtered := make([]minitui.SelectOption, 0, len(options))
	for _, option := range options {
		if strings.Contains(strings.ToLower(option.Label), strings.ToLower(query)) {
			filtered = append(filtered, option)
		}
	}
	return filtered
}
