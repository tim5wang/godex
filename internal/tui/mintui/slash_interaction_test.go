package mintui

import (
	"testing"

	"github.com/tim5wang/godex/internal/services/commands"
	minitui "github.com/tim5wang/min-tui"
)

type recordingSlashSelector struct {
	selections []int
	prompts    []string
	options    [][]minitui.SelectOption
	answers    []string
}

func (s *recordingSlashSelector) Select(prompt string, options []minitui.SelectOption) int {
	s.prompts = append(s.prompts, prompt)
	s.options = append(s.options, append([]minitui.SelectOption(nil), options...))
	if len(s.selections) == 0 {
		return -1
	}
	selected := s.selections[0]
	s.selections = s.selections[1:]
	return selected
}

func (s *recordingSlashSelector) Prompt(prompt string) string {
	s.prompts = append(s.prompts, prompt)
	if len(s.answers) == 0 {
		return ""
	}
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer
}

func slashCommandMetadata(t *testing.T, name string) commands.CommandMetadata {
	t.Helper()
	for _, item := range commands.AvailableMetadata() {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("missing /%s metadata", name)
	return commands.CommandMetadata{}
}

func TestCompleteSlashArgumentsSelectsSkillSubcommandAndCandidate(t *testing.T) {
	selector := &recordingSlashSelector{selections: []int{5, 1}}
	command := slashCommandMetadata(t, "skills")
	args, proceed, err := completeSlashArguments(selector, command, "", func(source string, _ []string) ([]minitui.SelectOption, error) {
		if source != "skills" {
			t.Fatalf("expected skills candidate source, got %q", source)
		}
		return []minitui.SelectOption{
			{Label: "playwright", Description: "Browser automation"},
			{Label: "python", Description: "Python development"},
		}, nil
	})
	if err != nil {
		t.Fatalf("complete slash arguments: %v", err)
	}
	if !proceed || args != "load python" {
		t.Fatalf("expected load python, proceed=%v args=%q", proceed, args)
	}
	if len(selector.options) != 2 || selector.options[0][5].Label != "load" || selector.options[1][1].Label != "python" {
		t.Fatalf("unexpected selection menus: %+v", selector.options)
	}
}

func TestCompleteSlashArgumentsFiltersMCPServerCandidates(t *testing.T) {
	selector := &recordingSlashSelector{selections: []int{0}}
	command := slashCommandMetadata(t, "mcp")
	args, proceed, err := completeSlashArguments(selector, command, "tools web", func(source string, _ []string) ([]minitui.SelectOption, error) {
		if source != "mcp_servers" {
			t.Fatalf("expected MCP candidate source, got %q", source)
		}
		return []minitui.SelectOption{
			{Label: "crm"},
			{Label: "web-search"},
		}, nil
	})
	if err != nil {
		t.Fatalf("complete slash arguments: %v", err)
	}
	if !proceed || args != "tools web-search" {
		t.Fatalf("expected tools web-search, proceed=%v args=%q", proceed, args)
	}
	if len(selector.options) != 1 || len(selector.options[0]) != 1 || selector.options[0][0].Label != "web-search" {
		t.Fatalf("expected filtered server options, got %+v", selector.options)
	}
}

func TestCompleteSlashArgumentsKeepsExactCandidateWithoutPrompt(t *testing.T) {
	selector := &recordingSlashSelector{}
	args, proceed, err := completeSlashArguments(selector, slashCommandMetadata(t, "mcp"), "load crm", func(string, []string) ([]minitui.SelectOption, error) {
		return []minitui.SelectOption{{Label: "crm"}}, nil
	})
	if err != nil {
		t.Fatalf("complete slash arguments: %v", err)
	}
	if !proceed || args != "load crm" || len(selector.options) != 0 {
		t.Fatalf("expected exact candidate to dispatch directly, proceed=%v args=%q options=%+v", proceed, args, selector.options)
	}
}

func TestCompleteSlashArgumentsSelectsSkillAndSection(t *testing.T) {
	selector := &recordingSlashSelector{selections: []int{0, 1}}
	args, proceed, err := completeSlashArguments(selector, slashCommandMetadata(t, "skills"), "expand", func(source string, previous []string) ([]minitui.SelectOption, error) {
		switch source {
		case "skills":
			return []minitui.SelectOption{{Label: "playwright"}}, nil
		case "skill_sections":
			if len(previous) != 1 || previous[0] != "playwright" {
				t.Fatalf("expected selected skill as section context, got %v", previous)
			}
			return []minitui.SelectOption{{Label: "browser"}, {Label: "testing"}}, nil
		default:
			t.Fatalf("unexpected candidate source %q", source)
			return nil, nil
		}
	})
	if err != nil {
		t.Fatalf("complete slash arguments: %v", err)
	}
	if !proceed || args != "expand playwright testing" {
		t.Fatalf("expected expand playwright testing, proceed=%v args=%q", proceed, args)
	}
}

func TestCompleteSlashArgumentsPromptsForRequiredFreeformArgument(t *testing.T) {
	selector := &recordingSlashSelector{
		selections: []int{4},
		answers:    []string{"https://github.com/acme/demo"},
	}
	args, proceed, err := completeSlashArguments(selector, slashCommandMetadata(t, "skills"), "", func(string, []string) ([]minitui.SelectOption, error) {
		t.Fatal("freeform argument should not request candidates")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("complete slash arguments: %v", err)
	}
	if !proceed || args != "install https://github.com/acme/demo" {
		t.Fatalf("expected install source prompt, proceed=%v args=%q", proceed, args)
	}
	if len(selector.prompts) != 2 {
		t.Fatalf("expected a subcommand menu and one required-argument prompt, got %v", selector.prompts)
	}
}
