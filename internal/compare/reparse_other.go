//go:build !windows

package compare

import (
	"os"
	"path/filepath"
	"strings"
)

func validRootPath(path string) bool {
	return filepath.IsAbs(path) && !strings.ContainsRune(path, 0)
}

func unsupported(info os.FileInfo) bool {
	return info == nil || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}
func openCaptureRoot(path string) (*os.Root, error) { return os.OpenRoot(path) }
