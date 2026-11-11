package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSkillActivationRejectsDeclaredUnsupportedPlatform(t *testing.T) {
	a := newTestAgent(t, 100000)
	other := "windows"
	if runtime.GOOS == other {
		other = "linux"
	}
	dir := filepath.Join(a.cfg.SkillsDir, "native-only")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: native-only\ndescription: platform workflow\nsupported_os: ["+other+"]\n---\nNever activate on the wrong platform."), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ActivateSkill("native-only"); err == nil {
		t.Fatal("unsupported skill activated")
	}
	if len(a.ActiveSkillNames()) != 0 {
		t.Fatal("unsupported core leaked into active prompt")
	}
}
