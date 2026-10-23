package compare

import "os"

// OpenConfinedRoot opens an existing local directory by pinning each Windows
// path component and rejecting reparse points, including a Git service root.
// The caller owns the returned handle and must close it. Capture and the local
// journal share this primitive; later operations must use the returned os.Root.
func OpenConfinedRoot(path string) (*os.Root, error) {
	if !validRootPath(path) {
		return nil, fail("INVALID_ROOT", "Нужен абсолютный локальный путь диска; сетевые пути и пространства устройств не поддерживаются.")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || unsupported(info) {
		return nil, fail("INVALID_ROOT", "Выбранная папка недоступна либо является ссылкой.")
	}
	root, err := openCaptureRoot(path)
	if err != nil {
		return nil, fail("ROOT_UNAVAILABLE", "Не удалось безопасно открыть выбранную папку.")
	}
	opened, err := root.Lstat(".")
	if err != nil || !sameObservation(info, opened) {
		root.Close()
		return nil, fail("SOURCE_CHANGED", "Папка изменилась при открытии. Повторите операцию.")
	}
	return root, nil
}
