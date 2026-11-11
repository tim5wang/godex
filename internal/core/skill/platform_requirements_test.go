package skill

import (
	"runtime"
	"strings"
	"testing"
)

func TestWindowsCompatibilityExampleDoesNotImposePlatformRequirement(t *testing.T) {
	parsed, err := parseSkill("ppt", "SKILL.md", "---\nname: ppt\ndescription: slides\n---\nRun python3 render.py.\nOn Windows, if python3 is unavailable, use python.")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate metadata produced by the old keyword-only normalizer.
	parsed.Requires.OSHints = []string{"windows"}
	parsed.Compatibility = Compatibility{Status: CompatibilityDegradedSupported, Notes: []string{"Contains OS-specific assumptions."}}
	parsed.Warnings = []string{"Current runtime OS is darwin, but this skill contains windows-specific guidance."}
	applyRuntimeCompatibility(parsed)
	if len(parsed.Requires.OSHints) != 0 || len(parsed.Requires.SupportedOS) != 0 || len(parsed.Warnings) != 0 || parsed.Compatibility.Status != CompatibilityNativeSupported {
		t.Fatalf("example treated as a platform restriction: %+v", parsed)
	}
}

func TestDeclaredSupportedOSSurvivesNormalizationAndIsEnforced(t *testing.T) {
	other := "windows"
	if runtime.GOOS == other {
		other = "linux"
	}
	parsed, err := parseSkill("native", "SKILL.md", "---\nname: native\ndescription: native helper\nsupported_os: ["+other+"]\n---\nPlatform-specific workflow.")
	if err != nil {
		t.Fatal(err)
	}
	doc, core := buildDeterministicNormalized(parsed)
	applyNormalized(parsed, normalizedArtifacts{Document: doc, Core: core})
	applyRuntimeCompatibility(parsed)
	if parsed.Compatibility.Status != CompatibilityUnsupported || len(parsed.Requires.SupportedOS) != 1 || !strings.Contains(strings.Join(parsed.Warnings, " "), "supported_os") {
		t.Fatalf("hard platform requirement lost: %+v", parsed)
	}
}

func TestExecutionHintsRemainAdvisory(t *testing.T) {
	parsed, err := parseSkill("example", "SKILL.md", "Run powershell ./build.ps1 for the Windows example.")
	if err != nil {
		t.Fatal(err)
	}
	applyRuntimeCompatibility(parsed)
	if len(parsed.Requires.OSHints) != 1 || len(parsed.Requires.SupportedOS) != 0 || parsed.Compatibility.Status == CompatibilityUnsupported {
		t.Fatalf("execution example became a hard requirement: %+v", parsed)
	}
}

func TestNormalizationCannotInventHardPlatformRequirements(t *testing.T) {
	parsed, err := parseSkill("portable", "SKILL.md", "Portable workflow.")
	if err != nil {
		t.Fatal(err)
	}
	doc, core := buildDeterministicNormalized(parsed)
	doc.Requires.SupportedOS = []string{"windows"}
	applyNormalized(parsed, normalizedArtifacts{Document: doc, Core: core})
	if len(parsed.Requires.SupportedOS) != 0 {
		t.Fatalf("normalizer invented a platform restriction: %+v", parsed.Requires)
	}
}
