//go:build windows

package workspace

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"
)

func TestApplyNamedStreamRejectsBeforeProjectNamespaceChanges(t *testing.T) {
	local := fixture(t, map[string]string{"z-existing.txt": "old\n"})
	incoming := fixture(t, map[string]string{"a-new.txt": "new file\n", "z-existing.txt": "new\n"})
	streamPath := filepath.Join(local.Root, "z-existing.txt:Zone.Identifier")
	streamBytes := []byte("[ZoneTransfer]\r\nZoneId=3\r\n")
	if err := os.WriteFile(streamPath, streamBytes, 0600); err != nil {
		t.Fatalf("native ADS fixture unavailable: %v", err)
	}
	plan, err := Plan(&local.Manifest, local, incoming, nil)
	if err != nil || !plan.CanApply {
		t.Fatalf("fixture plan: %+v %v", plan, err)
	}
	result, err := Apply(context.Background(), plan, incoming, t.TempDir())
	if code(err) != "UNSUPPORTED_STREAMS" || result.RecoveryRequired {
		t.Fatalf("ADS apply was not rejected before mutation: %+v %v", result, err)
	}
	entries, err := os.ReadDir(local.Root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "z-existing.txt" {
		t.Fatalf("rejected apply changed the project namespace: %v %v", entries, err)
	}
	if contents(t, local.Root, "z-existing.txt") != "old\n" {
		t.Fatal("rejected apply changed the main stream")
	}
	actual, err := os.ReadFile(streamPath)
	if err != nil || !bytes.Equal(actual, streamBytes) {
		t.Fatal("rejected apply changed the named stream")
	}
}

func TestVerifyNoNamedStreamsNativeFixtures(t *testing.T) {
	for _, stream := range []string{"", "Zone.Identifier", "user-data"} {
		name := stream
		if name == "" {
			name = "default-only"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "file.txt")
			main, extra := []byte("main fixture\r\n"), []byte("named stream fixture\n")
			if err := os.WriteFile(path, main, 0600); err != nil {
				t.Fatal(err)
			}
			if stream != "" {
				if err := os.WriteFile(path+":"+stream, extra, 0600); err != nil {
					t.Fatalf("native ADS fixture unavailable: %v", err)
				}
			}
			file, err := openLocked(path, false)
			if err != nil {
				t.Fatal(err)
			}
			err = verifyNoNamedStreams(file)
			file.close()
			if stream == "" && err != nil || stream != "" && code(err) != "UNSUPPORTED_STREAMS" {
				t.Fatalf("guard result: %v", err)
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, main) {
				t.Fatal("guard changed the main stream")
			}
			if stream != "" {
				actual, err := os.ReadFile(path + ":" + stream)
				if err != nil || !bytes.Equal(actual, extra) {
					t.Fatal("guard changed the named stream")
				}
			}
		})
	}
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	file, err := openLocked(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyNoNamedStreams(file); err != nil {
		file.close()
		t.Fatalf("empty default stream rejected: %v", err)
	}
	file.close()
	for _, invalid := range []*lockedFile{nil, {}, file} {
		if code(verifyNoNamedStreams(invalid)) != "UNSUPPORTED_STREAMS" {
			t.Fatal("invalid/closed handle accepted")
		}
	}
}

func streamInfoFixture(name string, next uint32) []byte {
	wide := utf16.Encode([]rune(name))
	size := 24 + len(wide)*2
	if int(next) > size {
		size = int(next)
	}
	buffer := make([]byte, size)
	binary.LittleEndian.PutUint32(buffer[:4], next)
	binary.LittleEndian.PutUint32(buffer[4:8], uint32(len(wide)*2))
	for i, unit := range wide {
		binary.LittleEndian.PutUint16(buffer[24+i*2:], unit)
	}
	return buffer
}

func TestFileStreamInfoParserRejectsUnknownAndMalformedMetadata(t *testing.T) {
	valid := streamInfoFixture("::$DATA", 0)
	padded := append(append([]byte{}, valid...), make([]byte, maxStreamInfoBytes-len(valid))...)
	for _, buffer := range [][]byte{valid, padded} {
		if err := validateDefaultStreamInfo(buffer); err != nil {
			t.Fatalf("default metadata rejected: %v", err)
		}
	}
	mutate := func(change func([]byte)) []byte {
		result := append([]byte{}, valid...)
		change(result)
		return result
	}
	cases := map[string][]byte{
		"empty":                 nil,
		"truncated-header":      valid[:23],
		"truncated-name":        valid[:len(valid)-1],
		"too-large-buffer":      make([]byte, maxStreamInfoBytes+1),
		"zero-length-name":      streamInfoFixture("", 0),
		"named-stream":          streamInfoFixture(":Zone.Identifier:$DATA", 0),
		"unknown-stream-type":   streamInfoFixture("::$UNKNOWN", 0),
		"extra-named-stream":    append(streamInfoFixture("::$DATA", 40), streamInfoFixture(":data:$DATA", 0)...),
		"duplicate-default":     append(streamInfoFixture("::$DATA", 40), valid...),
		"odd-name-length":       mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[4:8], 13) }),
		"oversized-name-length": mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[4:8], ^uint32(0)) }),
		"backward-next-offset":  mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[:4], 8) }),
		"unaligned-next-offset": mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[:4], 39) }),
		"next-outside-buffer":   mutate(func(b []byte) { binary.LittleEndian.PutUint32(b[:4], ^uint32(7)) }),
		"truncated-next-record": streamInfoFixture("::$DATA", 40),
		"negative-stream-size":  mutate(func(b []byte) { binary.LittleEndian.PutUint64(b[8:16], 1<<63) }),
		"negative-allocation":   mutate(func(b []byte) { binary.LittleEndian.PutUint64(b[16:24], 1<<63) }),
	}
	for name, buffer := range cases {
		t.Run(name, func(t *testing.T) {
			if code(validateDefaultStreamInfo(buffer)) != "UNSUPPORTED_STREAMS" {
				t.Fatal("unsupported metadata accepted")
			}
		})
	}
}
