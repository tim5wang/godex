package tools

import "testing"

func TestLongRunningToolsExposeTimeoutSeconds(t *testing.T) {
	workspace := t.TempDir()
	tools := []Tool{
		NewBashTool(workspace),
		NewGrepToolWithBackend(&deadlineGrepBackend{}),
		NewFindTool(workspace),
		NewSkillTool(&fakeSkillRuntime{}),
		NewInstallPackageTool(&timeoutPackageRuntime{}),
		NewCompressTool(&timeoutConversationManager{}),
	}
	for _, tool := range tools {
		properties, ok := tool.Spec().InputSchema["properties"].(map[string]interface{})
		if !ok {
			t.Fatalf("%s: missing properties schema", tool.Name())
		}
		if _, ok := properties["timeout_seconds"]; !ok {
			t.Errorf("%s: missing timeout_seconds schema", tool.Name())
		}
	}
}
