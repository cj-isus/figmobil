//go:build !windows

package secure

import (
	"errors"
	"testing"
)

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	if _, err := ProtectData([]byte("synthetic")); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err := UnprotectData([]byte("synthetic")); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	if release, err := AcquireOwner(); !errors.Is(err, ErrUnsupported) || release != nil {
		t.Fatal("unsupported platform acquired owner")
	}
}
