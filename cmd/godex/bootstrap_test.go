package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInformationalCommandsSkipConfigAndRuntime(t *testing.T) {
	for _, command := range []string{"version", "--version", "help", "--help", "-h"} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("GODEX_HOME", root)
			// Invalid config must not prevent a version/help query.
			if err := os.WriteFile(filepath.Join(root, "godex.yaml"), []byte("invalid: ["), 0600); err != nil {
				t.Fatal(err)
			}
			output, err := os.CreateTemp(t.TempDir(), "output")
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			originalArgs, originalOutput := os.Args, os.Stdout
			os.Args, os.Stdout = []string{"godex", command}, output
			defer func() { os.Args, os.Stdout = originalArgs, originalOutput }()
			_, _, done, err := prepareRuntimeArgs()
			if err != nil || !done {
				t.Fatalf("informational command initialized runtime: done=%v err=%v", done, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("informational command wrote runtime state: %v %v", entries, err)
			}
		})
	}
}
