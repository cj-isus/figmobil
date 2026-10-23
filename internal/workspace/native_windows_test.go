//go:build windows

package workspace

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"figmobil/internal/compare"
)

func TestCOWPreservesProtectedDACLAndOwnerGroup(t *testing.T) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		t.Fatal(err)
	}
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "inherited", true: "protected"}[protected], func(t *testing.T) {
			root := t.TempDir()
			file, err := openLocked(filepath.Join(root, "source.txt"), true)
			if err != nil {
				t.Fatal(err)
			}
			if err := replaceLocked(context.Background(), file, []byte("old\n")); err != nil {
				t.Fatal(err)
			}
			sddl, _ := syscall.UTF16PtrFromString("D:(A;;FA;;;" + sid + ")(A;;FA;;;SY)(A;;FA;;;BA)")
			var descriptor uintptr
			var size uint32
			convert := securityAPI.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
			ok, _, err := convert.Call(uintptr(unsafe.Pointer(sddl)), 1, uintptr(unsafe.Pointer(&descriptor)), uintptr(unsafe.Pointer(&size)))
			if ok == 0 {
				t.Fatal(err)
			}
			defer syscall.LocalFree(syscall.Handle(descriptor))
			var acl uintptr
			var present, defaulted uint32
			ok, _, err = getDescriptorDACL.Call(descriptor, uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&acl)), uintptr(unsafe.Pointer(&defaulted)))
			if ok == 0 || present == 0 {
				t.Fatal(err)
			}
			flags := uint32(4 | 0x20000000)
			if protected {
				flags = 4 | 0x80000000
			}
			code, _, _ := setSecurityInfo.Call(file.file.Fd(), 1, uintptr(flags), 0, 0, acl, 0)
			if code != 0 {
				t.Fatal(syscall.Errno(code))
			}
			before, err := readSecurity(file.file)
			if err != nil {
				t.Fatal(err)
			}
			var control uint16
			var revision uint32
			getDescriptorControl.Call(uintptr(unsafe.Pointer(&before[0])), uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
			if (control&0x1000 != 0) != protected {
				t.Fatalf("fixture protection=%#x", control)
			}
			file.close()
			local, err := compare.Capture(context.Background(), root, nil)
			if err != nil {
				t.Fatal(err)
			}
			incoming := fixture(t, map[string]string{"source.txt": "new\n"})
			plan, err := Plan(&local.Manifest, local, incoming, nil)
			if err != nil || !plan.CanApply {
				t.Fatalf("plan: %+v %v", plan, err)
			}
			if _, err := Apply(context.Background(), plan, incoming, t.TempDir()); err != nil {
				t.Fatal(err)
			}
			afterFile, err := os.Open(filepath.Join(root, "source.txt"))
			if err != nil {
				t.Fatal(err)
			}
			defer afterFile.Close()
			after, err := readSecurity(afterFile)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("owner/group/DACL/control changed: %v", err)
			}
		})
	}
}

func TestNativeReparseMutationFailsClosed(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	parent := filepath.Join(root, "child")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	dirs := newDirectoryLocks()
	dirs.scopeRoot = root
	defer dirs.close()
	if err := dirs.lock(parent, false); err != nil {
		t.Fatal(err)
	}
	handle, err := syscall.CreateFile(nativePath(parent), 0x100|0x100000, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatalf("metadata writer fixture: %v", err)
	}
	defer syscall.CloseHandle(handle)
	substitute, _ := syscall.UTF16FromString(`\??\` + outside)
	printName, _ := syscall.UTF16FromString(outside)
	buffer := make([]byte, 16+2*(len(substitute)+len(printName)))
	binary.LittleEndian.PutUint32(buffer, 0xA0000003)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(len(buffer)-8))
	binary.LittleEndian.PutUint16(buffer[10:], uint16(2*(len(substitute)-1)))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(2*len(substitute)))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(2*(len(printName)-1)))
	for index, value := range append(substitute, printName...) {
		binary.LittleEndian.PutUint16(buffer[16+2*index:], value)
	}
	var returned uint32
	err = syscall.DeviceIoControl(handle, 0x900A4, &buffer[0], uint32(len(buffer)), nil, 0, &returned, nil)
	if err != nil {
		t.Fatalf("native mount point mutation fixture failed: %v", err)
	}
	if err := dirs.verify(); err == nil {
		t.Fatal("native reparse mutation was not detected")
	}
	if file, err := dirs.openFile(filepath.Join(parent, "escaped.txt"), true); err == nil {
		file.disposition(true)
		file.close()
		t.Fatal("mutated parent accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside root modified: %v %v", entries, err)
	}
}

func TestShortDirectoryAliasesCannotEnterExcludedScope(t *testing.T) {
	root := t.TempDir()
	long := filepath.Join(root, "excluded-directory-with-long-name")
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	wide, _ := syscall.UTF16PtrFromString(long)
	buffer := make([]uint16, 32768)
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")
	length, _, err := proc.Call(uintptr(unsafe.Pointer(wide)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 || length >= uintptr(len(buffer)) {
		t.Fatal(err)
	}
	short := syscall.UTF16ToString(buffer[:length])
	if strings.EqualFold(filepath.Base(short), filepath.Base(long)) {
		t.Skip("fixture volume has no alternate 8.3 name; canonical-name guard remains covered by normal native opens")
	}
	dirs := newDirectoryLocks()
	dirs.scopeRoot = root
	defer dirs.close()
	if err := dirs.lock(filepath.Join(root, filepath.Base(short)), false); err == nil {
		t.Fatal("short alias entered a directory outside manifest's lexical scope")
	}
	entries, err := os.ReadDir(long)
	if err != nil || len(entries) != 0 {
		t.Fatal("excluded directory modified")
	}
}

func TestJournalAliasCannotCreateRecoveryDirectoryInsideProject(t *testing.T) {
	parent := t.TempDir()
	long := filepath.Join(parent, "project-directory-with-long-name")
	if err := os.Mkdir(long, 0700); err != nil {
		t.Fatal(err)
	}
	wide, _ := syscall.UTF16PtrFromString(long)
	buffer := make([]uint16, 32768)
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetShortPathNameW")
	length, _, err := proc.Call(uintptr(unsafe.Pointer(wide)), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if length == 0 || length >= uintptr(len(buffer)) {
		t.Fatal(err)
	}
	short := syscall.UTF16ToString(buffer[:length])
	if strings.EqualFold(filepath.Base(short), filepath.Base(long)) {
		t.Skip("volume has no alternate 8.3 name")
	}
	dirs := newDirectoryLocks()
	dirs.scopeRoot = short
	defer dirs.close()
	if err := dirs.lock(short, false); err != nil {
		t.Fatal(err)
	}
	if err := dirs.lockJournal(filepath.Join(long, "must-not-be-created"), true); err == nil {
		t.Fatal("journal entered project through an alias")
	}
	entries, err := os.ReadDir(long)
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected journal created project entries: %v %v", entries, err)
	}
}
