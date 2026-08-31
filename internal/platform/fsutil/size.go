package fsutil

import (
	"os"
	"path/filepath"
)

// DirSizeBestEffort returns the total size of non-directory entries below
// path. Entries that cannot be walked or inspected are ignored.
func DirSizeBestEffort(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
