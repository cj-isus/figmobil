//go:build windows

package workspace

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

type lockedFile struct {
	file *os.File
	path string
}
type directoryLocks struct {
	files         []*os.File
	paths         map[string]bool
	byPath        map[string]*os.File
	scopeRoot     string
	forbiddenRoot os.FileInfo
}

var setFileInformation = syscall.NewLazyDLL("kernel32.dll").NewProc("SetFileInformationByHandle")
var securityAPI = syscall.NewLazyDLL("advapi32.dll")
var getKernelSecurity = securityAPI.NewProc("GetKernelObjectSecurity")
var setKernelSecurity = securityAPI.NewProc("SetKernelObjectSecurity")
var setSecurityInfo = securityAPI.NewProc("SetSecurityInfo")
var getDescriptorControl = securityAPI.NewProc("GetSecurityDescriptorControl")
var getDescriptorOwner = securityAPI.NewProc("GetSecurityDescriptorOwner")
var getDescriptorGroup = securityAPI.NewProc("GetSecurityDescriptorGroup")
var getDescriptorDACL = securityAPI.NewProc("GetSecurityDescriptorDacl")
var getFinalPath = syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")
var nativeAPI = syscall.NewLazyDLL("ntdll.dll")
var ntSetInformation = nativeAPI.NewProc("NtSetInformationFile")
var ntErrorCode = nativeAPI.NewProc("RtlNtStatusToDosError")
var ntCreate = nativeAPI.NewProc("NtCreateFile")

func openRelative(parent *os.File, name string, directory, create bool) (syscall.Handle, error) {
	wide, err := syscall.UTF16FromString(name)
	if err != nil || len(wide) > 32760 {
		return syscall.InvalidHandle, syscall.EINVAL
	}
	unicode := struct {
		Length, MaximumLength uint16
		Buffer                *uint16
	}{uint16((len(wide) - 1) * 2), uint16(len(wide) * 2), &wide[0]}
	attrs := struct {
		Length            uint32
		Root              syscall.Handle
		Name              unsafe.Pointer
		Attributes        uint32
		Security, Quality uintptr
	}{Root: syscall.Handle(parent.Fd()), Name: unsafe.Pointer(&unicode), Attributes: 0x40}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	access := uint32(syscall.GENERIC_READ | 0x100000)
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE)
	options := uint32(0x200000 | 0x20)
	disposition := uint32(1)
	if directory {
		options |= 1 | 0x4000
	} else {
		access |= syscall.GENERIC_WRITE | 0x10000
		share = syscall.FILE_SHARE_READ
		options |= 0x40
		if create {
			access |= 0x40000 | 0x80000
		}
	}
	if create {
		disposition = 2
	}
	var handle syscall.Handle
	status := struct{ Status, Information uintptr }{}
	code, _, _ := ntCreate.Call(uintptr(unsafe.Pointer(&handle)), uintptr(access), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&status)), 0, 0x80, uintptr(share), uintptr(disposition), uintptr(options), 0, 0)
	if int32(code) < 0 {
		mapped, _, _ := ntErrorCode.Call(code)
		return syscall.InvalidHandle, syscall.Errno(mapped)
	}
	return handle, nil
}

func nativePath(path string) *uint16 {
	value, _ := syscall.UTF16PtrFromString(`\\?\` + path)
	return value
}
func openLocked(path string, create bool) (*lockedFile, error) {
	mode := uint32(syscall.OPEN_EXISTING)
	if create {
		mode = syscall.CREATE_NEW
	}
	access := uint32(syscall.GENERIC_READ | syscall.GENERIC_WRITE | 0x00010000)
	if create {
		access |= 0x00040000 | 0x00080000
	}
	handle, err := syscall.CreateFile(nativePath(path), access, syscall.FILE_SHARE_READ, nil, mode, syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fail("FILE_BUSY", "Файл занят, недоступен или его состояние изменилось. Закройте редактор и повторите предпросмотр.")
	}
	file := os.NewFile(uintptr(handle), path)
	var info syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&(syscall.FILE_ATTRIBUTE_REPARSE_POINT|syscall.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		file.Close()
		return nil, fail("UNSAFE_PATH", "Неподдерживаемый тип файла.")
	}
	if info.NumberOfLinks != 1 {
		file.Close()
		return nil, fail("UNSUPPORTED_LINK", "Файл имеет жёсткие ссылки: запись могла бы изменить другой путь.")
	}
	return &lockedFile{file, path}, nil
}
func (f *lockedFile) close() {
	if f != nil && f.file != nil {
		_ = f.file.Close()
		f.file = nil
	}
}
func (f *lockedFile) disposition(deleted bool) error {
	value := uint32(0)
	if deleted {
		value = 1
	}
	ok, _, err := setFileInformation.Call(f.file.Fd(), 4, uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value))
	if ok == 0 {
		return err
	}
	return nil
}

func (f *lockedFile) renameTo(target string) error { return f.renameWithFlags(target, 0) }
func (f *lockedFile) renameWithFlags(target string, flags uint32) error {
	return f.renameAt(nil, target, flags)
}
func (f *lockedFile) renameAt(parent *os.File, target string, flags uint32) error {
	if parent == nil && filepath.IsAbs(target) {
		target = "\\??\\" + target
	}
	name, err := syscall.UTF16FromString(target)
	if err != nil {
		return err
	}
	if len(name) > 32760 {
		return fail("INVALID_PATH", "Слишком длинный путь замены.")
	}
	info := struct {
		Flags         uint32
		RootDirectory syscall.Handle
		Length        uint32
		Name          [32768]uint16
	}{Flags: flags, Length: uint32((len(name) - 1) * 2)}
	if parent != nil {
		info.RootDirectory = syscall.Handle(parent.Fd())
	}
	copy(info.Name[:], name)
	size := unsafe.Offsetof(info.Name) + uintptr(info.Length)
	status := struct{ Status, Information uintptr }{}
	code, _, _ := ntSetInformation.Call(f.file.Fd(), uintptr(unsafe.Pointer(&status)), uintptr(unsafe.Pointer(&info)), size, 65)
	if int32(code) < 0 {
		mapped, _, _ := ntErrorCode.Call(code)
		return syscall.Errno(mapped)
	}
	f.path = target
	return nil
}

func copySecurity(source, target *lockedFile) error {
	data, err := readSecurity(source.file)
	if err != nil {
		return err
	}
	var control uint16
	var revision uint32
	var owner, group, dacl uintptr
	var ignored, present uint32
	ok, _, _ := getDescriptorControl.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision)))
	if ok == 0 {
		return fail("UNSUPPORTED_SECURITY", "Не удалось проверить управление ACL исходного файла.")
	}
	for _, field := range []struct {
		proc *syscall.LazyProc
		out  *uintptr
	}{{getDescriptorOwner, &owner}, {getDescriptorGroup, &group}} {
		ok, _, _ = field.proc.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(field.out)), uintptr(unsafe.Pointer(&ignored)))
		if ok == 0 {
			return fail("UNSUPPORTED_SECURITY", "Не удалось проверить владельца или группу файла.")
		}
	}
	ok, _, _ = getDescriptorDACL.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(unsafe.Pointer(&present)), uintptr(unsafe.Pointer(&dacl)), uintptr(unsafe.Pointer(&ignored)))
	if ok == 0 || present == 0 {
		return fail("UNSUPPORTED_SECURITY", "ACL исходного файла не поддерживается.")
	}
	flags := uint32(7 | 0x20000000) // OWNER/GROUP/DACL, unprotected DACL.
	if control&0x1000 != 0 {
		flags = 7 | 0x80000000
	}
	code, _, _ := setSecurityInfo.Call(target.file.Fd(), 1, uintptr(flags), owner, group, dacl, 0)
	if code != 0 {
		return fail("UNSUPPORTED_SECURITY", "Не удалось сохранить владельца, группу и ACL файла.")
	}
	after, err := readSecurity(target.file)
	if err != nil || !bytes.Equal(data, after) {
		return fail("UNSUPPORTED_SECURITY", "Права или наследование ACL не сохранились точно; замена остановлена.")
	}
	var old syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(syscall.Handle(source.file.Fd()), &old) != nil {
		return fail("UNSUPPORTED_SECURITY", "Не удалось проверить атрибуты файла.")
	}
	basic := struct {
		Creation, Access, Write, Change int64
		Attributes                      uint32
	}{Attributes: old.FileAttributes}
	ok, _, _ = setFileInformation.Call(target.file.Fd(), 0, uintptr(unsafe.Pointer(&basic)), unsafe.Sizeof(basic))
	if ok == 0 {
		return fail("UNSUPPORTED_SECURITY", "Не удалось сохранить атрибуты файла.")
	}
	return nil
}

func readSecurity(file *os.File) ([]byte, error) {
	var needed uint32
	getKernelSecurity.Call(file.Fd(), 7, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 || needed > 65536 {
		return nil, fail("UNSUPPORTED_SECURITY", "Не удалось ограниченно прочитать права исходного файла.")
	}
	data := make([]byte, needed)
	ok, _, _ := getKernelSecurity.Call(file.Fd(), 7, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return nil, fail("UNSUPPORTED_SECURITY", "Не удалось прочитать права исходного файла.")
	}
	return data[:needed], nil
}

func renameJournal(root *os.Root, source, target string) error {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	handle, err := openRelative(parent, source, false, false)
	if err != nil {
		return err
	}
	file := &lockedFile{file: os.NewFile(uintptr(handle), source), path: source}
	defer file.close()
	return file.renameAt(parent, target, 1)
}
func newDirectoryLocks() *directoryLocks {
	return &directoryLocks{paths: map[string]bool{}, byPath: map[string]*os.File{}}
}
func (d *directoryLocks) lock(path string, create bool) error {
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || !filepath.IsAbs(path) || strings.HasPrefix(volume, `\`) {
		return fail("INVALID_ROOT", "Требуется обычный локальный путь диска Windows.")
	}
	current := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator))
	all := []string{current}
	for _, part := range parts {
		if part != "" {
			current = filepath.Join(current, part)
			all = append(all, current)
		}
	}
	for _, name := range all {
		key := strings.ToLower(name)
		if d.paths[key] {
			continue
		}
		var handle syscall.Handle
		var err error
		if name == volume+string(filepath.Separator) {
			handle, err = syscall.CreateFile(nativePath(name), syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		} else {
			parent := d.byPath[strings.ToLower(filepath.Dir(name))]
			handle, err = openRelative(parent, filepath.Base(name), true, false)
		}
		if err != nil && create && os.IsNotExist(err) {
			handle, err = openRelative(d.byPath[strings.ToLower(filepath.Dir(name))], filepath.Base(name), true, true)
		}
		if err != nil {
			return fail("DIRECTORY_BUSY", "Каталог занят или недоступен; применение остановлено.")
		}
		var info syscall.ByHandleFileInformation
		if syscall.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			syscall.CloseHandle(handle)
			return fail("UNSAFE_PATH", "Ссылки и точки повторной обработки в пути записи запрещены.")
		}
		file := os.NewFile(uintptr(handle), name)
		if d.forbiddenRoot != nil {
			opened, err := file.Stat()
			if err != nil || os.SameFile(d.forbiddenRoot, opened) {
				file.Close()
				return fail("INVALID_JOURNAL", "Журнал физически находится в проекте; создание остановлено.")
			}
		}
		actual, err := canonicalName(handle)
		if err != nil || strings.EqualFold(actual, ".git") || d.withinScope(name) && !strings.EqualFold(actual, filepath.Base(name)) {
			file.Close()
			return fail("UNSAFE_PATH", "Короткие имена и псевдонимы каталогов проекта запрещены.")
		}
		d.files = append(d.files, file)
		d.byPath[key] = file
		d.paths[key] = true
	}
	return nil
}
func (d *directoryLocks) openFile(target string, create bool) (*lockedFile, error) {
	if err := d.verify(); err != nil {
		return nil, err
	}
	parent := d.byPath[strings.ToLower(filepath.Dir(target))]
	if parent == nil {
		return nil, fail("UNSAFE_PATH", "Родительский каталог не закреплён.")
	}
	handle, err := openRelative(parent, filepath.Base(target), false, create)
	if err != nil {
		return nil, fail("FILE_BUSY", "Файл занят, недоступен или его состояние изменилось.")
	}
	file := os.NewFile(uintptr(handle), target)
	actual, nameErr := canonicalName(handle)
	if nameErr != nil || !strings.EqualFold(actual, filepath.Base(target)) {
		file.Close()
		return nil, fail("UNSAFE_PATH", "Короткие имена и псевдонимы файлов проекта запрещены.")
	}
	var info syscall.ByHandleFileInformation
	if syscall.GetFileInformationByHandle(handle, &info) != nil || info.FileAttributes&(syscall.FILE_ATTRIBUTE_DIRECTORY|syscall.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		file.Close()
		return nil, fail("UNSAFE_PATH", "Неподдерживаемый тип файла.")
	}
	if info.NumberOfLinks != 1 {
		file.Close()
		return nil, fail("UNSUPPORTED_LINK", "Жёсткие ссылки требуют отдельной проверки.")
	}
	return &lockedFile{file, target}, nil
}

func canonicalName(handle syscall.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	length, _, err := getFinalPath.Call(uintptr(handle), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
	if length == 0 || length >= uintptr(len(buffer)) {
		return "", err
	}
	return filepath.Base(syscall.UTF16ToString(buffer[:length])), nil
}
func (d *directoryLocks) withinScope(path string) bool {
	if d.scopeRoot == "" {
		return false
	}
	relative, err := filepath.Rel(d.scopeRoot, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Compare pinned directory identities as well as spelling: an 8.3 name or a
// local drive alias must not put recovery data inside the selected project.
func (d *directoryLocks) requireDisjoint(first, second string) error {
	contains := func(ancestor, child string) (bool, error) {
		root := d.byPath[strings.ToLower(filepath.Clean(ancestor))]
		if root == nil {
			return false, fail("UNSAFE_PATH", "Каталог не закреплён.")
		}
		identity, err := root.Stat()
		if err != nil {
			return false, err
		}
		for name := filepath.Clean(child); ; name = filepath.Dir(name) {
			file := d.byPath[strings.ToLower(name)]
			if file == nil {
				return false, fail("UNSAFE_PATH", "Родительский каталог не закреплён.")
			}
			info, err := file.Stat()
			if err != nil {
				return false, err
			}
			if os.SameFile(identity, info) {
				return true, nil
			}
			if filepath.Dir(name) == name {
				return false, nil
			}
		}
	}
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		overlap, err := contains(pair[0], pair[1])
		if err != nil {
			return err
		}
		if overlap {
			return fail("INVALID_JOURNAL", "Журнал и проект физически пересекаются.")
		}
	}
	return nil
}

func (d *directoryLocks) lockJournal(path string, create bool) error {
	root := d.byPath[strings.ToLower(filepath.Clean(d.scopeRoot))]
	if root == nil {
		return fail("UNSAFE_PATH", "Корень проекта не закреплён.")
	}
	info, err := root.Stat()
	if err != nil {
		return err
	}
	d.forbiddenRoot = info
	defer func() { d.forbiddenRoot = nil }()
	return d.lock(path, create)
}
func (d *directoryLocks) renameFile(file *lockedFile, target string) error {
	if err := d.verify(); err != nil {
		return err
	}
	parent := d.byPath[strings.ToLower(filepath.Dir(target))]
	if parent == nil {
		return fail("UNSAFE_PATH", "Родительский каталог не закреплён.")
	}
	if err := file.renameAt(parent, filepath.Base(target), 0); err != nil {
		return err
	}
	file.path = target
	return nil
}
func (d *directoryLocks) verify() error {
	for _, file := range d.files {
		var info syscall.ByHandleFileInformation
		if syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info) != nil || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return fail("UNSAFE_PATH", "Каталог превратился в точку повторной обработки.")
		}
	}
	return nil
}
func (d *directoryLocks) close() {
	for i := len(d.files) - 1; i >= 0; i-- {
		_ = d.files[i].Close()
	}
	d.files = nil
}
