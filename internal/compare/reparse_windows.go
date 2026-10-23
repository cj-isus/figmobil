//go:build windows

package compare

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

var finalCapturePath = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")
var captureDriveType = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

func validRootPath(path string) bool {
	if !validRootSyntax(path) {
		return false
	}
	volume, err := syscall.UTF16PtrFromString(path[:2] + `\`)
	if err != nil {
		return false
	}
	kind, _, _ := captureDriveType.Call(uintptr(unsafe.Pointer(volume)))
	runtime.KeepAlive(volume)
	return localDriveKind(kind)
}

func validRootSyntax(path string) bool {
	if len(path) < 3 || strings.ContainsRune(path, 0) {
		return false
	}
	letter := path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z'
	return letter && path[1] == ':' && (path[2] == '\\' || path[2] == '/') && filepath.IsAbs(path) && filepath.VolumeName(path) == path[:2]
}

// Drive-letter mappings to SMB remain remote even though they are not UNC text.
func localDriveKind(kind uintptr) bool { return kind == 2 || kind == 3 || kind == 6 }

// Pin each directory component before descending. A reparse mutation between
// Lstat and OpenRoot either fails confinement or fails the identity check; the
// next component is always resolved from the already-open parent directory.
func openCaptureRoot(path string) (*os.Root, error) {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	current, err := os.OpenRoot(volume + string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	relative := strings.TrimPrefix(clean, volume+string(filepath.Separator))
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		before, err := current.Lstat(component)
		if err != nil || unsupported(before) || !before.IsDir() {
			current.Close()
			return nil, fail("UNSAFE_PATH", "Путь проекта содержит неподдерживаемую ссылку.")
		}
		next, err := current.OpenRoot(component)
		if err != nil {
			current.Close()
			return nil, err
		}
		opened, err := next.Lstat(".")
		if err != nil || unsupported(opened) || !os.SameFile(before, opened) {
			next.Close()
			current.Close()
			return nil, fail("SOURCE_CHANGED", "Путь проекта изменился при открытии.")
		}
		file, err := next.Open(".")
		if err != nil {
			next.Close()
			current.Close()
			return nil, err
		}
		buffer := make([]uint16, 32768)
		length, _, finalErr := finalCapturePath.Call(file.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
		file.Close()
		if length == 0 || length >= uintptr(len(buffer)) {
			next.Close()
			current.Close()
			return nil, finalErr
		}
		if strings.EqualFold(filepath.Base(syscall.UTF16ToString(buffer[:length])), ".git") {
			next.Close()
			current.Close()
			return nil, fail("UNSAFE_PATH", "Служебный каталог Git не может быть корнем проекта или его предком.")
		}
		current.Close()
		current = next
	}
	return current, nil
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
