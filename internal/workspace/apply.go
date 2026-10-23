package workspace

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"figmobil/internal/compare"
	"figmobil/internal/secure"
)

type ApplyResult struct {
	Status           string   `json:"status"`
	JournalPath      string   `json:"journalPath,omitempty"`
	Changed          []string `json:"changed"`
	RecoveryRequired bool     `json:"recoveryRequired"`
}
type journalEntry struct {
	Path       string `json:"path"`
	Action     string `json:"action"`
	BeforeHash string `json:"beforeHash"`
	AfterHash  string `json:"afterHash"`
	BeforeBlob string `json:"beforeBlob"`
	AfterBlob  string `json:"afterBlob"`
	Aside      string `json:"aside"`
	Prepared   string `json:"prepared"`
}
type journal struct {
	Version int            `json:"version"`
	Root    string         `json:"root"`
	State   string         `json:"state"`
	Entries []journalEntry `json:"entries"`
}

// Apply accepts only an unmodified backend-owned plan. Existing files are held
// open with Windows deny-write/delete sharing during precheck/write/rollback.
// New bytes are prepared in a separate file with verified ACL/mode preservation.
// Own-handle, no-replace renames publish it without modifying the old stream.
// The namespace gap and a process crash require explicit journal recovery;
// unknown or concurrently created versions are preserved. This is not atomic.
func Apply(ctx context.Context, plan ApplyPlan, incoming compare.Snapshot, journalDir string) (ApplyResult, error) {
	return apply(ctx, plan, incoming, journalDir, nil)
}

func apply(ctx context.Context, plan ApplyPlan, incoming compare.Snapshot, journalDir string, beforeWrite func(string) error) (result ApplyResult, err error) {
	result = ApplyResult{Status: "rejected", Changed: []string{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !plan.CanApply || plan.seal != sealPlan(plan) || plan.ID != planID(plan) {
		return result, fail("INVALID_PLAN", "План не подтверждён, заблокирован или изменён.")
	}
	if err := compare.ValidateSnapshot(incoming); err != nil {
		return result, err
	}
	if incoming.Manifest.ID != plan.IncomingID {
		return result, fail("SNAPSHOT_MISMATCH", "Входящий снимок не соответствует плану.")
	}
	rootPath := filepath.Clean(plan.Root)
	journalDir = filepath.Clean(journalDir)
	if !filepath.IsAbs(journalDir) || pathsOverlap(rootPath, journalDir) {
		return result, fail("INVALID_JOURNAL", "Журнал должен находиться в отдельной локальной папке приложения.")
	}
	marker := filepath.Join(journalDir, "journal.json")
	if _, statErr := os.Lstat(marker); statErr == nil {
		return result, fail("RECOVERY_PENDING", "Сначала завершите явное восстановление предыдущей операции.")
	} else if !os.IsNotExist(statErr) {
		return result, fail("JOURNAL_UNAVAILABLE", "Не удалось проверить журнал.")
	}
	dirs := newDirectoryLocks()
	dirs.scopeRoot = rootPath
	defer dirs.close()
	if err := dirs.lock(rootPath, false); err != nil {
		return result, err
	}
	current, err := compare.Capture(ctx, rootPath, plan.excludes)
	if err != nil {
		return result, err
	}
	if current.Manifest.ID != plan.LocalID {
		return result, fail("SOURCE_CHANGED", "Локальный проект изменился после предпросмотра.")
	}
	paths := make([]string, 0, len(incoming.Files))
	for name := range incoming.Files {
		paths = append(paths, name)
	}
	policy := compare.ReadGitPolicy(ctx, rootPath, paths)
	if !policy.Supported || policy.Fingerprint != incoming.Manifest.Policy.Fingerprint {
		return result, fail("UNSUPPORTED_POLICY", "Политика Git изменилась или не поддерживается.")
	}
	items := []PlanItem{}
	for _, item := range plan.Items {
		if item.Action != "skip" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		result.Status = "no-change"
		return result, nil
	}
	if len(items) > 5000 {
		return result, fail("APPLY_LIMIT", "Слишком много операций применения.")
	}
	if err := dirs.lockJournal(journalDir, true); err != nil {
		return result, err
	}
	if err := dirs.requireDisjoint(rootPath, journalDir); err != nil {
		return result, err
	}
	journalRoot, err := compare.OpenConfinedRoot(journalDir)
	if err != nil {
		return result, fail("JOURNAL_UNAVAILABLE", "Не удалось открыть папку журнала.")
	}
	defer journalRoot.Close()
	files := map[string]*lockedFile{}
	defer func() {
		for _, file := range files {
			file.close()
		}
	}()
	state := journal{Version: 1, Root: rootPath, State: "prepared", Entries: []journalEntry{}}
	createdBlobs := []string{}
	prepared := false
	defer func() {
		if !prepared {
			for _, name := range createdBlobs {
				_ = journalRoot.Remove(name)
			}
		}
	}()
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := compare.ValidateRelativePath(item.Path); err != nil {
			return result, err
		}
		target := filepath.Join(rootPath, filepath.FromSlash(item.Path))
		if item.Before != nil {
			if err := dirs.lock(filepath.Dir(target), false); err != nil {
				return result, err
			}
			file, err := dirs.openFile(target, false)
			if err != nil {
				return result, err
			}
			files[item.Path] = file
			if err := verifyNoNamedStreams(file); err != nil {
				return result, err
			}
			data, err := readLocked(file)
			if err != nil || hash(data) != item.Before.SHA256 {
				return result, fail("SOURCE_CHANGED", "Файл изменился после предпросмотра.")
			}
			blob, err := writeProtected(journalRoot, data)
			if err != nil {
				return result, err
			}
			createdBlobs = append(createdBlobs, blob)
			entry := journalEntry{Path: item.Path, Action: item.Action, BeforeHash: hash(data), BeforeBlob: blob}
			if item.After != nil {
				entry.AfterHash = item.After.SHA256
				blob, err = writeProtected(journalRoot, incoming.Files[item.Path])
				if err != nil {
					return result, err
				}
				createdBlobs = append(createdBlobs, blob)
				entry.AfterBlob = blob
			}
			state.Entries = append(state.Entries, entry)
		} else {
			if item.After == nil || item.After.Mode != "0666" {
				return result, fail("UNSUPPORTED_MODE", "Создание файла с этим режимом пока не поддерживается.")
			}
			blob, err := writeProtected(journalRoot, incoming.Files[item.Path])
			if err != nil {
				return result, err
			}
			createdBlobs = append(createdBlobs, blob)
			state.Entries = append(state.Entries, journalEntry{Path: item.Path, Action: item.Action, AfterHash: item.After.SHA256, AfterBlob: blob})
		}
	}
	for index := range state.Entries {
		entry := &state.Entries[index]
		parent := filepath.Dir(entry.Path)
		if parent == "." {
			parent = ""
		}
		entry.Aside = filepath.ToSlash(filepath.Join(parent, ".figmobil-old-"+randomName()))
		entry.Prepared = filepath.ToSlash(filepath.Join(parent, ".figmobil-new-"+randomName()))
	}
	if err := saveJournal(journalRoot, state); err != nil {
		return result, err
	}
	prepared = true
	result.JournalPath = marker
	result.RecoveryRequired = true
	state.State = "applying"
	if err := saveJournal(journalRoot, state); err != nil {
		return result, err
	}
	applied := []journalEntry{}
	oldFiles := map[string]*lockedFile{}
	defer func() {
		for _, file := range oldFiles {
			file.close()
		}
	}()
	for _, entry := range state.Entries {
		if err = ctx.Err(); err != nil {
			break
		}
		if beforeWrite != nil {
			if err = beforeWrite(entry.Path); err != nil {
				break
			}
		}
		if err = dirs.verify(); err != nil {
			break
		}
		target := filepath.Join(rootPath, filepath.FromSlash(entry.Path))
		if err = dirs.lock(filepath.Dir(target), true); err != nil {
			break
		}
		if entry.Action != "delete" {
			var data []byte
			data, err = readProtected(journalRoot, entry.AfterBlob)
			if err == nil && hash(data) != entry.AfterHash {
				err = fail("JOURNAL_CORRUPTED", "Повреждены данные подготовленного файла.")
			}
			if err != nil {
				break
			}
			var prepared *lockedFile
			prepared, err = dirs.openFile(filepath.Join(rootPath, filepath.FromSlash(entry.Prepared)), true)
			if err != nil {
				break
			}
			if old := files[entry.Path]; old != nil {
				err = copySecurity(old, prepared)
			}
			if err == nil {
				err = replaceLocked(ctx, prepared, data)
			}
			if err != nil {
				_ = prepared.disposition(true)
				prepared.close()
				break
			}
			oldFiles[entry.Path] = files[entry.Path]
			files[entry.Path] = prepared
		}
		old := oldFiles[entry.Path]
		if entry.Action == "delete" {
			old = files[entry.Path]
			oldFiles[entry.Path] = old
			files[entry.Path] = nil
		}
		if old != nil {
			if err = dirs.renameFile(old, filepath.Join(rootPath, filepath.FromSlash(entry.Aside))); err != nil {
				if prepared := files[entry.Path]; prepared != nil {
					_ = prepared.disposition(true)
					prepared.close()
				}
				files[entry.Path] = old
				delete(oldFiles, entry.Path)
				break
			}
		}
		applied = append(applied, entry)
		if entry.Action != "delete" {
			err = dirs.renameFile(files[entry.Path], target)
			if err != nil {
				break
			}
		}
		if err = dirs.verify(); err != nil {
			break
		}
		result.Changed = append(result.Changed, entry.Path)
	}
	if err == nil {
		policy = compare.ReadGitPolicy(ctx, rootPath, paths)
		if !policy.Supported || policy.Fingerprint != incoming.Manifest.Policy.Fingerprint {
			err = fail("POLICY_CHANGED", "Политика Git изменилась во время применения.")
		}
	}
	if err != nil {
		rollbackErr := rollbackCopies(dirs, rootPath, applied, files, oldFiles)
		if rollbackErr != nil {
			result.Status = "recovery-required"
			return result, errors.Join(err, rollbackErr)
		}
		state.State = "rolled-back"
		if saveErr := saveJournal(journalRoot, state); saveErr != nil {
			return result, errors.Join(err, saveErr)
		}
		result.Status = "rolled-back"
		result.Changed = []string{}
		return result, err
	}
	state.State = "committed"
	if err := saveJournal(journalRoot, state); err != nil {
		return result, err
	}
	// Finalize pending deletions before removing the committed marker.
	for _, file := range oldFiles {
		if file != nil {
			if err := file.disposition(true); err != nil {
				return result, err
			}
			file.close()
		}
	}
	for _, file := range files {
		file.close()
	}
	if err := cleanupJournal(journalRoot, state); err != nil {
		result.Status = "committed"
		return result, err
	}
	result.Status = "applied"
	result.RecoveryRequired = false
	result.JournalPath = ""
	return result, nil
}

func rollbackCopies(dirs *directoryLocks, rootPath string, entries []journalEntry, files, oldFiles map[string]*lockedFile) error {
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if current := files[entry.Path]; current != nil {
			if err := current.disposition(true); err != nil {
				return err
			}
			current.close()
			files[entry.Path] = nil
		}
		if old := oldFiles[entry.Path]; old != nil {
			if err := dirs.renameFile(old, filepath.Join(rootPath, filepath.FromSlash(entry.Path))); err != nil {
				return fail("RECOVERY_CONFLICT", "Исходное имя занято новой версией. Она сохранена; прежняя версия остаётся в журнале и временном файле.")
			}
			files[entry.Path] = old
			oldFiles[entry.Path] = nil
		}
	}
	return nil
}

// Recover never writes a pre-existing stream. Known after-images are removed
// only after all paths pass validation; originals are restored by no-replace
// rename from their recorded aside names. Unknown newer user files stay intact.
func Recover(ctx context.Context, journalPath string) (ApplyResult, error) {
	result := ApplyResult{Status: "recovery-required", JournalPath: journalPath, Changed: []string{}, RecoveryRequired: true}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if filepath.Base(journalPath) != "journal.json" || !filepath.IsAbs(journalPath) {
		return result, fail("INVALID_JOURNAL", "Недопустимый путь журнала.")
	}
	dirs := newDirectoryLocks()
	defer dirs.close()
	if err := dirs.lock(filepath.Dir(journalPath), false); err != nil {
		return result, err
	}
	root, err := compare.OpenConfinedRoot(filepath.Dir(journalPath))
	if err != nil {
		return result, err
	}
	defer root.Close()
	data, err := readProtected(root, "journal.json")
	if err != nil {
		return result, err
	}
	var state journal
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || state.Version != 1 || len(state.Entries) > 5000 || !filepath.IsAbs(state.Root) || pathsOverlap(state.Root, filepath.Dir(journalPath)) {
		return result, fail("JOURNAL_CORRUPTED", "Журнал не прошёл проверку.")
	}
	if state.State != "prepared" && state.State != "applying" && state.State != "committed" && state.State != "rolled-back" {
		return result, fail("JOURNAL_CORRUPTED", "Неизвестное состояние журнала.")
	}
	dirs.scopeRoot = state.Root
	if err := dirs.lock(state.Root, false); err != nil {
		return result, err
	}
	if err := dirs.requireDisjoint(state.Root, filepath.Dir(journalPath)); err != nil {
		return result, err
	}
	files := map[string]*lockedFile{}
	oldFiles := map[string]*lockedFile{}
	privateFiles := []*lockedFile{}
	defer func() {
		for _, f := range files {
			f.close()
		}
		for _, f := range oldFiles {
			f.close()
		}
		for _, f := range privateFiles {
			f.close()
		}
	}()
	restore := []journalEntry{}
	seen := map[string]bool{}
	for _, entry := range state.Entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := compare.ValidateRelativePath(entry.Path); err != nil {
			return result, err
		}
		if seen[strings.ToLower(entry.Path)] || !validPrivateName(entry.Path, entry.Aside, ".figmobil-old-") || !validPrivateName(entry.Path, entry.Prepared, ".figmobil-new-") {
			return result, fail("JOURNAL_CORRUPTED", "Недопустимые пути журнала.")
		}
		seen[strings.ToLower(entry.Path)] = true
		if entry.Action != "create" && entry.Action != "replace" && entry.Action != "delete" {
			return result, fail("JOURNAL_CORRUPTED", "Неизвестная операция журнала.")
		}
		parent := filepath.Join(state.Root, filepath.FromSlash(filepath.Dir(entry.Path)))
		if _, err := os.Lstat(parent); os.IsNotExist(err) && entry.Action == "create" {
			continue
		}
		if err := dirs.lock(parent, false); err != nil {
			return result, err
		}
		openOptional := func(relative string) (*lockedFile, error) {
			target := filepath.Join(state.Root, filepath.FromSlash(relative))
			// Existence check is only advisory. The actual open uses the pinned
			// parent; a concurrent create/remove still fails hash/no-replace checks.
			if _, err := os.Lstat(target); os.IsNotExist(err) {
				return nil, nil
			}
			file, err := dirs.openFile(target, false)
			if err != nil {
				return nil, err
			}
			if err := verifyNoNamedStreams(file); err != nil {
				file.close()
				return nil, err
			}
			return file, nil
		}
		aside, err := openOptional(entry.Aside)
		if err != nil {
			return result, err
		}
		if aside != nil {
			oldFiles[entry.Path] = aside
			bytes, err := readLocked(aside)
			if err != nil || hash(bytes) != entry.BeforeHash {
				return result, fail("RECOVERY_CONFLICT", "Временная исходная версия изменена; восстановление остановлено.")
			}
		}
		prepared, err := openOptional(entry.Prepared)
		if err != nil {
			return result, err
		}
		if prepared != nil {
			privateFiles = append(privateFiles, prepared)
		}
		if state.State == "committed" || state.State == "rolled-back" {
			continue
		}
		current, err := openOptional(entry.Path)
		if err != nil {
			return result, err
		}
		if current == nil {
			if entry.BeforeHash != "" && aside == nil {
				return result, fail("RECOVERY_CONFLICT", "Исходный файл и его временная версия отсутствуют. Автоматическое восстановление остановлено.")
			}
			if aside != nil {
				restore = append(restore, entry)
			}
			continue
		}
		files[entry.Path] = current
		bytes, err := readLocked(current)
		if err != nil {
			return result, err
		}
		actual := hash(bytes)
		if actual == entry.BeforeHash {
			continue
		}
		if actual != entry.AfterHash {
			return result, fail("RECOVERY_CONFLICT", "Файл содержит новую или неизвестную версию. Она сохранена; перезапись не выполнялась.")
		}
		if entry.BeforeHash != "" && aside == nil {
			return result, fail("RECOVERY_CONFLICT", "Временная исходная версия отсутствует; зашифрованный резерв сохранён для ручного восстановления.")
		}
		restore = append(restore, entry)
	}
	if state.State != "committed" && state.State != "rolled-back" {
		if err := rollbackCopies(dirs, state.Root, restore, files, oldFiles); err != nil {
			return result, err
		}
		state.State = "rolled-back"
		if err := saveJournal(root, state); err != nil {
			return result, err
		}
	}
	// These are sealed, random, application-owned resource names, never package
	// paths. Partial prepared files are safe to discard; originals are never
	// rewritten and current user files were validated before any mutation.
	for _, f := range privateFiles {
		if err := f.disposition(true); err != nil {
			return result, err
		}
		f.close()
	}
	for _, f := range oldFiles {
		if f != nil {
			if err := f.disposition(true); err != nil {
				return result, err
			}
			f.close()
		}
	}
	for _, f := range files {
		f.close()
	}
	if err := cleanupJournal(root, state); err != nil {
		return result, err
	}
	result.Status = "recovered"
	result.RecoveryRequired = false
	result.JournalPath = ""
	return result, nil
}

func validPrivateName(original, resource, prefix string) bool {
	if compare.ValidateRelativePath(resource) != nil || filepath.Dir(original) != filepath.Dir(resource) {
		return false
	}
	name := filepath.Base(resource)
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(name, prefix))
	return err == nil
}

func replaceLocked(ctx context.Context, file *lockedFile, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := file.file.Truncate(0); err != nil {
		return fail("WRITE_FAILED", "Не удалось подготовить файл к записи.")
	}
	if _, err := file.file.Seek(0, 0); err != nil {
		return err
	}
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+(32<<10), len(data))
		n, err := file.file.Write(data[offset:end])
		if err != nil || n == 0 {
			return fail("WRITE_FAILED", "Не удалось записать файл целиком.")
		}
		offset += n
	}
	if err := file.file.Sync(); err != nil {
		return fail("WRITE_FAILED", "Не удалось подтвердить запись файла.")
	}
	return nil
}
func readLocked(file *lockedFile) ([]byte, error) {
	if _, err := file.file.Seek(0, 0); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file.file, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return nil, fail("READ_FAILED", "Не удалось проверить ограниченный файл.")
	}
	return data, nil
}
func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func pathsOverlap(a, b string) bool {
	a = strings.ToLower(filepath.Clean(a))
	b = strings.ToLower(filepath.Clean(b))
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}
func randomName() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}
func writeProtected(root *os.Root, data []byte) (string, error) {
	// DPAPI's generic helper requires a nonempty input. A versioned encrypted
	// frame represents even a zero-byte source without inventing file bytes.
	framed := append([]byte(blobFrame), data...)
	protected, err := secure.ProtectData(framed)
	if err != nil {
		return "", err
	}
	name := "blob-" + randomName() + ".bin"
	if err := writeNew(root, name, protected); err != nil {
		return "", err
	}
	return name, nil
}
func readProtected(root *os.Root, name string) ([]byte, error) {
	if name != "journal.json" && (!strings.HasPrefix(name, "blob-") || strings.ContainsAny(name, "/\\:") || len(name) != 41) {
		return nil, fail("JOURNAL_CORRUPTED", "Недопустимый файл журнала.")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (40<<20)+1))
	if err != nil || len(data) > 40<<20 {
		return nil, fail("JOURNAL_CORRUPTED", "Файл журнала превышает лимит.")
	}
	clear, err := secure.UnprotectData(data)
	if err != nil || name == "journal.json" {
		return clear, err
	}
	if len(clear) < len(blobFrame) || string(clear[:len(blobFrame)]) != blobFrame {
		return nil, fail("JOURNAL_CORRUPTED", "Неизвестный формат зашифрованного резерва.")
	}
	return clear[len(blobFrame):], nil
}

const blobFrame = "figmobil-journal-blob-v1\x00"

func writeNew(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}
func saveJournal(root *os.Root, state journal) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	data, err = secure.ProtectData(data)
	if err != nil {
		return err
	}
	name := "journal-tmp-" + randomName()
	if err := writeNew(root, name, data); err != nil {
		return err
	}
	if err := renameJournal(root, name, "journal.json"); err != nil {
		_ = root.Remove(name)
		return err
	}
	return nil
}
func cleanupJournal(root *os.Root, state journal) error {
	for _, entry := range state.Entries {
		for _, name := range []string{entry.BeforeBlob, entry.AfterBlob} {
			if name == "" {
				continue
			}
			if !strings.HasPrefix(name, "blob-") || strings.ContainsAny(name, "/\\:") || len(name) != 41 {
				return fail("JOURNAL_CORRUPTED", "Недопустимый резервный файл.")
			}
			if err := root.Remove(name); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return root.Remove("journal.json")
}
