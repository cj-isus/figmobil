//go:build windows

package secure

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This device-wide name is deliberately independent of application version,
// project, Windows session and profile. All app versions must use the same name.
const ownerMutexName = `Global\Figmobil.DeviceOwner`

var localEntropy = []byte("figmobil.local-protection.v1")

func dataBlob(data []byte) windows.DataBlob {
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func copyAndFree(blob windows.DataBlob, limit uint32) ([]byte, error) {
	if blob.Data == nil {
		return nil, ErrProtection
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))
	data := unsafe.Slice(blob.Data, blob.Size)
	defer clear(data)
	if blob.Size == 0 || blob.Size > limit {
		return nil, ErrSize
	}
	return append([]byte(nil), data...), nil
}

// ProtectData applies Windows DPAPI CurrentUser, never LOCAL_MACHINE, without
// prompts. It returns an opaque blob and performs no filesystem operation.
func ProtectData(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 || len(plaintext) > MaxLocalDataBytes {
		return nil, ErrSize
	}
	input, entropy := dataBlob(plaintext), dataBlob(localEntropy)
	var output windows.DataBlob
	err := windows.CryptProtectData(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(plaintext)
	runtime.KeepAlive(localEntropy)
	if err != nil {
		return nil, ErrProtection
	}
	return copyAndFree(output, MaxProtectedBytes)
}

func UnprotectData(protected []byte) ([]byte, error) {
	if len(protected) == 0 || len(protected) > MaxProtectedBytes {
		return nil, ErrSize
	}
	input, entropy := dataBlob(protected), dataBlob(localEntropy)
	var output windows.DataBlob
	err := windows.CryptUnprotectData(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &output)
	runtime.KeepAlive(protected)
	runtime.KeepAlive(localEntropy)
	if err != nil {
		return nil, ErrProtection
	}
	return copyAndFree(output, MaxLocalDataBytes)
}

// AcquireOwner atomically creates the one device-wide owner object. Its handle
// lifetime is the lease: the mutex is not thread-owned and never waited on, so
// Go goroutine migration cannot release another thread's ownership. Existing or
// inaccessible objects fail closed. Call release only when the owner has stopped
// every transfer operation; construct a new rate gate only after acquisition.
func AcquireOwner() (func(), error) {
	name, err := windows.UTF16PtrFromString(ownerMutexName)
	if err != nil {
		return nil, ErrOwnerUnavailable
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, ErrOwnerExists
		}
		return nil, ErrOwnerUnavailable
	}
	if handle == 0 {
		return nil, ErrOwnerUnavailable
	}
	var once sync.Once
	return func() { once.Do(func() { _ = windows.CloseHandle(handle) }) }, nil
}
