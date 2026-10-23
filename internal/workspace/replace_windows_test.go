//go:build windows

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeNoReplaceCOWWithHeldOldHandle(t *testing.T) {
	root := t.TempDir()
	dirs := newDirectoryLocks()
	defer dirs.close()
	if err := dirs.lock(root, false); err != nil {
		t.Fatal(err)
	}
	put(t, root, "old.txt", "old\n")
	old, err := openLocked(filepath.Join(root, "old.txt"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer old.close()
	prepared, err := openLocked(filepath.Join(root, "prepared.txt"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.close()
	if err := replaceLocked(context.Background(), prepared, []byte("new\n")); err != nil {
		t.Fatal(err)
	}
	if err := copySecurity(old, prepared); err != nil {
		t.Fatal(err)
	}
	if err := old.renameAt(dirs.files[len(dirs.files)-1], "aside.txt", 0); err != nil {
		t.Fatalf("own-handle rename failed: %v", err)
	}
	if err := prepared.renameAt(dirs.files[len(dirs.files)-1], "old.txt", 0); err != nil {
		t.Fatalf("native no-replace rename into vacant name failed: %v", err)
	}
	data, err := readLocked(old)
	if err != nil || string(data) != "old\n" {
		t.Fatalf("old stream changed: %q %v", data, err)
	}
	prepared.close()
	if contents(t, root, "old.txt") != "new\n" {
		t.Fatal("new name does not read prepared stream")
	}
}

func TestNoReplaceAndRollbackPreserveConcurrentCreator(t *testing.T) {
	root := t.TempDir()
	dirs := newDirectoryLocks()
	dirs.scopeRoot = root
	defer dirs.close()
	if err := dirs.lock(root, false); err != nil {
		t.Fatal(err)
	}
	put(t, root, "source.txt", "old\n")
	old, err := dirs.openFile(filepath.Join(root, "source.txt"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer old.close()
	prepared, err := dirs.openFile(filepath.Join(root, "prepared.txt"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.close()
	if err := replaceLocked(context.Background(), prepared, []byte("incoming\n")); err != nil {
		t.Fatal(err)
	}
	if err := dirs.renameFile(old, filepath.Join(root, "aside.txt")); err != nil {
		t.Fatal(err)
	}
	put(t, root, "source.txt", "concurrent\n")
	if err := dirs.renameFile(prepared, filepath.Join(root, "source.txt")); err == nil {
		t.Fatal("publish replaced concurrently created file")
	}
	err = rollbackCopies(dirs, root, []journalEntry{{Path: "source.txt"}}, map[string]*lockedFile{"source.txt": prepared}, map[string]*lockedFile{"source.txt": old})
	if err == nil {
		t.Fatal("rollback replaced concurrently created file")
	}
	if contents(t, root, "source.txt") != "concurrent\n" {
		t.Fatal("concurrent version overwritten")
	}
	before, err := readLocked(old)
	if err != nil || string(before) != "old\n" {
		t.Fatalf("original not preserved: %q %v", before, err)
	}
	if _, err := os.Stat(filepath.Join(root, "prepared.txt")); !os.IsNotExist(err) {
		t.Fatalf("own prepared resource not discarded: %v", err)
	}
}
