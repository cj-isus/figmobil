//go:build windows

package compare

import "testing"

func TestRemoteDriveKindsAreRejectedBeforeFilesystemAccess(t *testing.T) {
	for _, kind := range []uintptr{0, 1, 4, 5} {
		if localDriveKind(kind) {
			t.Fatalf("non-local or unsupported drive type %d accepted", kind)
		}
	}
	for _, kind := range []uintptr{2, 3, 6} {
		if !localDriveKind(kind) {
			t.Fatalf("local drive type %d rejected", kind)
		}
	}
	if !validRootPath(t.TempDir()) {
		t.Fatal("actual local fixture drive rejected")
	}
}
