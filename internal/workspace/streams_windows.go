//go:build windows

package workspace

import (
	"bytes"
	"encoding/binary"
	"runtime"
	"syscall"
	"unsafe"
)

const maxStreamInfoBytes = 64 << 10

var getStreamInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFileInformationByHandleEx")

// verifyNoNamedStreams checks the held file object, never a path. COW replaces
// only the default stream, so an existing named stream is an unsupported file
// property rather than data the application may silently discard. Enumeration
// failure or metadata beyond the fixed buffer also fails closed.
func verifyNoNamedStreams(file *lockedFile) error {
	if file == nil || file.file == nil || getStreamInformation.Find() != nil {
		return streamGuardError()
	}
	// FILE_STREAM_INFO records require 8-byte alignment. A uint64 backing array
	// also keeps the native buffer separate from all source/stream contents.
	words := make([]uint64, maxStreamInfoBytes/8)
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(&words[0])), maxStreamInfoBytes)
	ok, _, _ := getStreamInformation.Call(file.file.Fd(), 7, uintptr(unsafe.Pointer(&words[0])), uintptr(len(buffer)))
	runtime.KeepAlive(words)
	runtime.KeepAlive(file.file)
	if ok == 0 {
		return streamGuardError()
	}
	return validateDefaultStreamInfo(buffer)
}

// FILE_STREAM_INFO consists of a 24-byte fixed header followed by a UTF-16
// name, with each subsequent record aligned on an 8-byte boundary. The API
// returns no used-byte count, so unused bytes after the terminal record are
// ignored. Only a single literal NTFS default stream is supported.
func validateDefaultStreamInfo(buffer []byte) error {
	const headerBytes = 24
	defaultName := []byte{':', 0, ':', 0, '$', 0, 'D', 0, 'A', 0, 'T', 0, 'A', 0}
	if len(buffer) < headerBytes || len(buffer) > maxStreamInfoBytes {
		return streamGuardError()
	}
	count := 0
	for offset := 0; ; {
		if offset > len(buffer)-headerBytes {
			return streamGuardError()
		}
		header := buffer[offset : offset+headerBytes]
		next := uint64(binary.LittleEndian.Uint32(header[:4]))
		nameBytes := uint64(binary.LittleEndian.Uint32(header[4:8]))
		remaining := uint64(len(buffer) - offset)
		if nameBytes%2 != 0 || nameBytes > remaining-headerBytes ||
			binary.LittleEndian.Uint64(header[8:16])>>63 != 0 ||
			binary.LittleEndian.Uint64(header[16:24])>>63 != 0 {
			return streamGuardError()
		}
		if next != 0 && (next%8 != 0 || next < headerBytes+nameBytes || next > remaining-headerBytes) {
			return streamGuardError()
		}
		if !bytes.Equal(buffer[offset+headerBytes:offset+headerBytes+int(nameBytes)], defaultName) {
			return streamGuardError()
		}
		count++
		if count != 1 {
			return streamGuardError()
		}
		if next == 0 {
			return nil
		}
		offset += int(next)
	}
}

func streamGuardError() error {
	return fail("UNSUPPORTED_STREAMS", "Не удалось подтвердить отсутствие дополнительных потоков данных. Файл с такими свойствами не поддерживается для применения.")
}
