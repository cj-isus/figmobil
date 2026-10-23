package compare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type scanBudget struct {
	limits         Limits
	files, entries int
	bytes          int64
	metadataBytes  int64
}
type observation struct {
	path string
	info os.FileInfo
}
type snapshot struct {
	root     *os.Root
	files    map[string]*Metadata
	dirs     map[string]bool
	observed []observation
	blocked  []string
	warnings []Warning
	scan     Scan
	collect  func(string, *Metadata, []byte) error
	excludes []string
}

func scanTree(ctx context.Context, path string, budget *scanBudget) (result *snapshot, err error) {
	return scanTreeWithOptions(ctx, path, budget, nil, nil)
}

func scanTreeWithOptions(ctx context.Context, path string, budget *scanBudget, excludes []string, collect func(string, *Metadata, []byte) error) (result *snapshot, err error) {
	root, err := OpenConfinedRoot(path)
	if err != nil {
		return nil, err
	}
	tree := &snapshot{root: root, files: map[string]*Metadata{}, dirs: map[string]bool{}, warnings: []Warning{}, excludes: excludes, collect: collect}
	defer func() {
		if err != nil {
			_ = root.Close()
		}
	}()
	if err = tree.walk(ctx, ".", budget, 0); err != nil {
		return nil, err
	}
	return tree, nil
}

func excluded(name string, isDir bool) bool {
	name = strings.ToLower(name)
	if strings.HasPrefix(name, ".figmobil-") || name == ".git" || name == ".artifacts" || name == "node_modules" || name == "dist" || name == "coverage" {
		return true
	}
	return strings.HasPrefix(name, ".env") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".pfx") || strings.HasSuffix(name, ".agekey") || strings.HasSuffix(name, ".fmstate")
}

func (s *snapshot) walk(ctx context.Context, path string, budget *scanBudget, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 128 {
		return fail("TREE_DEPTH_LIMIT", "Превышена допустимая глубина каталогов.")
	}
	before, err := s.root.Lstat(path)
	if err != nil {
		return fail("READ_FAILED", "Не удалось проверить папку; сравнение остановлено.")
	}
	if unsupported(before) {
		return s.skip(path, budget)
	}
	if !before.IsDir() {
		return fail("SOURCE_CHANGED", "Тип папки изменился во время сравнения.")
	}
	dir, err := s.root.Open(path)
	if err != nil {
		return fail("READ_FAILED", "Не удалось прочитать папку; сравнение остановлено.")
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil || !sameObservation(before, opened) {
		return fail("SOURCE_CHANGED", "Папка изменилась во время сравнения.")
	}
	s.observed = append(s.observed, observation{path, before})
	if path != "." {
		s.dirs[filepath.ToSlash(path)] = true
	}
	names := []string{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(128)
		for _, entry := range entries {
			budget.entries++
			if budget.entries > budget.limits.MaxFiles*4+1024 {
				return fail("ENTRY_LIMIT", "Слишком много элементов в выбранных папках.")
			}
			// Charge full relative paths before retaining even excluded names.
			if err := budget.reserveEntryPath(path, entry.Name()); err != nil {
				return err
			}
			names = append(names, entry.Name())
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fail("READ_FAILED", "Не удалось прочитать папку целиком.")
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := filepath.Join(path, name)
		info, err := s.root.Lstat(child)
		if err != nil {
			return fail("SOURCE_CHANGED", "Состав папки изменился. Повторите сравнение.")
		}
		if excluded(name, info.IsDir()) || excludedRelative(child, s.excludes) {
			s.scan.Excluded++
			if err := budget.appendWarning(&s.warnings, Warning{Code: "PATH_EXCLUDED", Path: filepath.ToSlash(child), Message: "Служебный каталог или потенциальный файл секретов исключён из области сравнения."}); err != nil {
				return err
			}
			continue
		}
		if unsupported(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
			if err := s.skip(child, budget); err != nil {
				return err
			}
			continue
		}
		if info.IsDir() {
			if err := s.walk(ctx, child, budget, depth+1); err != nil {
				return err
			}
			continue
		}
		if budget.files >= budget.limits.MaxFiles {
			return fail("FILE_COUNT_LIMIT", "Превышен лимит количества файлов.")
		}
		if info.Size() > budget.limits.MaxFileBytes {
			return fail("FILE_SIZE_LIMIT", "Один из файлов превышает лимит размера.")
		}
		if info.Size() > budget.limits.MaxTotalBytes-budget.bytes {
			return fail("TOTAL_SIZE_LIMIT", "Превышен общий лимит чтения файлов.")
		}
		metadata, err := s.read(ctx, child, info, budget)
		if err != nil {
			return err
		}
		s.files[filepath.ToSlash(child)] = metadata
		s.scan.Files++
		s.scan.Bytes += metadata.Size
		budget.files++
		budget.bytes += metadata.Size
		s.observed = append(s.observed, observation{child, info})
	}
	after, err := dir.Stat()
	if err != nil || !sameObservation(before, after) {
		return fail("SOURCE_CHANGED", "Папка изменилась во время чтения.")
	}
	return nil
}

func (s *snapshot) read(ctx context.Context, path string, before os.FileInfo, budget *scanBudget) (*Metadata, error) {
	file, err := s.root.Open(path)
	if err != nil {
		return nil, fail("READ_FAILED", "Не удалось открыть файл; сравнение остановлено.")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !sameObservation(before, opened) {
		return nil, fail("SOURCE_CHANGED", "Файл изменился при открытии.")
	}
	// Reading is bounded even when a file grows after Stat. A small fixed buffer
	// checks cancellation between reads, without retaining source in the report.
	maximum := min(budget.limits.MaxFileBytes, budget.limits.MaxTotalBytes-budget.bytes)
	data := make([]byte, 0, min(before.Size(), maximum))
	buffer := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := file.Read(buffer)
		if int64(len(data))+int64(n) > maximum {
			return nil, fail("SOURCE_CHANGED", "Размер файла изменился во время чтения; лимит превышен.")
		}
		data = append(data, buffer[:n]...)
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fail("READ_FAILED", "Не удалось прочитать файл целиком.")
		}
	}
	after, err := file.Stat()
	current, pathErr := s.root.Lstat(path)
	if err != nil || pathErr != nil || unsupported(current) || !sameObservation(before, after) || !sameObservation(before, current) || int64(len(data)) != before.Size() {
		return nil, fail("SOURCE_CHANGED", "Файл изменился во время чтения. Повторите сравнение.")
	}
	digest := sha256.Sum256(data)
	metadata := &Metadata{Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Mode: fmt.Sprintf("%04o", before.Mode().Perm())}
	analyzeRepresentation(data, metadata)
	if s.collect != nil {
		if err := s.collect(filepath.ToSlash(path), metadata, data); err != nil {
			return nil, err
		}
	}
	return metadata, nil
}

func (s *snapshot) skip(path string, budget *scanBudget) error {
	path = filepath.ToSlash(path)
	if err := budget.appendWarning(&s.warnings, Warning{Code: "UNSUPPORTED_FILE_TYPE", Path: path, Message: "Ссылка, точка повторной обработки или специальный файл пропущены. Пути под ними не сравнивались."}); err != nil {
		return err
	}
	s.blocked = append(s.blocked, path)
	s.scan.Skipped++
	return nil
}
func (s *snapshot) isBlocked(path string) bool {
	// An unreadable Foo must also block foo/child on the Windows target.
	path = strings.ToLower(filepath.ToSlash(path))
	for _, blocked := range s.blocked {
		blocked = strings.ToLower(filepath.ToSlash(blocked))
		if path == blocked || strings.HasPrefix(path, blocked+"/") {
			return true
		}
	}
	return false
}
func (s *snapshot) verify(ctx context.Context) error {
	for _, item := range s.observed {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := s.root.Lstat(item.path)
		if err != nil || unsupported(current) || !sameObservation(item.info, current) {
			return fail("SOURCE_CHANGED", "Выбранные файлы или папки изменились. Повторите сравнение.")
		}
	}
	return nil
}
func sameObservation(a, b os.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime() == b.ModTime()
}
