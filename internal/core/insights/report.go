package insights

import "strings"

type Report struct {
	AgentMDAdditions      []string
	SkillCandidates       []string
	BundleRecommendations []string
	Frictions             []string
}

func (r Report) Markdown() string {
	sections := []struct {
		Title string
		Items []string
	}{
		{Title: "AGENT.md Additions", Items: r.AgentMDAdditions},
		{Title: "Skill Candidates", Items: r.SkillCandidates},
		{Title: "Bundle Recommendations", Items: r.BundleRecommendations},
		{Title: "Frictions", Items: r.Frictions},
	}

	lines := []string{"# Insights"}
	for _, section := range sections {
		lines = append(lines, "", "## "+section.Title)
		if len(section.Items) == 0 {
			lines = append(lines, "- None yet.")
			continue
		}
		for _, item := range section.Items {
			lines = append(lines, "- "+item)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}
