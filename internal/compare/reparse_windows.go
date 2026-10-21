//go:build windows

package compare

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validRootPath(path string) bool {
	if len(path) < 3 || strings.ContainsRune(path, 0) {
		return false
	}
	letter := path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z'
	return letter && path[1] == ':' && (path[2] == '\\' || path[2] == '/') &&
		filepath.IsAbs(path) && filepath.VolumeName(path) == path[:2]
}

func unsupported(info os.FileInfo) bool {
	if info == nil {
		return true
	}
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	if attributes, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}
