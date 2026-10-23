//go:build !windows

package workspace

import "os"

type lockedFile struct {
	file *os.File
	path string
}
type directoryLocks struct{ scopeRoot string }

func openLocked(string, bool) (*lockedFile, error) {
	return nil, fail("UNSUPPORTED_PLATFORM", "Запись реализована только для Windows.")
}
func (f *lockedFile) close() {}
func (f *lockedFile) disposition(bool) error {
	return fail("UNSUPPORTED_PLATFORM", "Запись реализована только для Windows.")
}
func (f *lockedFile) renameTo(string) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func copySecurity(*lockedFile, *lockedFile) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func renameJournal(*os.Root, string, string) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func newDirectoryLocks() *directoryLocks { return &directoryLocks{} }
func (d *directoryLocks) lock(string, bool) error {
	return fail("UNSUPPORTED_PLATFORM", "Запись реализована только для Windows.")
}
func (d *directoryLocks) close() {}
func (d *directoryLocks) openFile(string, bool) (*lockedFile, error) {
	return nil, fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func (d *directoryLocks) renameFile(*lockedFile, string) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func (d *directoryLocks) verify() error { return fail("UNSUPPORTED_PLATFORM", "Только Windows.") }
func (d *directoryLocks) requireDisjoint(string, string) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func (d *directoryLocks) lockJournal(string, bool) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
func verifyNoNamedStreams(*lockedFile) error {
	return fail("UNSUPPORTED_PLATFORM", "Только Windows.")
}
