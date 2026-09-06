package tools

import (
	"context"
	"testing"
)

type timeoutPackageRuntime struct {
	installHadDeadline bool
}

func (r *timeoutPackageRuntime) ListPackages() ([]PackageEntry, error) { return nil, nil }
func (r *timeoutPackageRuntime) InstallPackageContext(ctx context.Context, _ string) (PackageEntry, error) {
	_, r.installHadDeadline = ctx.Deadline()
	return PackageEntry{Name: "example"}, nil
}
func (r *timeoutPackageRuntime) RemovePackage(string) (PackageEntry, error) {
	return PackageEntry{}, nil
}
func (r *timeoutPackageRuntime) ListPrompts(bool) ([]PromptEntry, error) { return nil, nil }
func (r *timeoutPackageRuntime) ListPackageCommands(bool) ([]PackageCommandEntry, error) {
	return nil, nil
}
func (r *timeoutPackageRuntime) ListPackageRoles(bool) ([]PackageRoleEntry, error) {
	return nil, nil
}

func TestInstallPackageToolAppliesTimeoutSeconds(t *testing.T) {
	runtime := &timeoutPackageRuntime{}
	tool := NewInstallPackageTool(runtime)
	if _, err := tool.Execute(context.Background(), map[string]interface{}{
		"source":          "owner/repo",
		"timeout_seconds": 1,
	}); err != nil {
		t.Fatalf("install package: %v", err)
	}
	if !runtime.installHadDeadline {
		t.Fatal("expected install context to have a deadline")
	}
}
