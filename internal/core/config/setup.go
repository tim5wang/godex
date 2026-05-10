package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

// WriteDefaultConfigFile writes the canonical default godex.yaml.
func WriteDefaultConfigFile(path string, overwrite bool) error {
	path = filepath.Clean(path)
	if path == "" || path == "." {
		return fmt.Errorf("missing config path")
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return os.ErrExist
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	rendered, err := renderConfigTemplate(defaultConfigFile())
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, rendered, 0600)
}
