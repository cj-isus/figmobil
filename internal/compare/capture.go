package compare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

type GitPolicy struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	Supported   bool   `json:"supported"`
	Reason      string `json:"reason,omitempty"`
}

type Manifest struct {
	Version  int                 `json:"version"`
	ID       string              `json:"id"`
	Excludes []string            `json:"excludes"`
	Files    map[string]Metadata `json:"files"`
	Policy   GitPolicy           `json:"policy"`
}

// Snapshot is backend data. File bytes and the absolute root are never included
// by its JSON encoder. A package codec must deliberately handle those bytes.
type Snapshot struct {
	Manifest Manifest          `json:"manifest"`
	Files    map[string][]byte `json:"-"`
	Root     string            `json:"-"`
}

func Capture(ctx context.Context, root string, excludes []string) (Snapshot, error) {
	return CaptureWithLimits(ctx, root, excludes, DefaultLimits())
}

func CaptureWithLimits(ctx context.Context, root string, excludes []string, limits Limits) (Snapshot, error) {
	for _, component := range strings.Split(filepath.ToSlash(filepath.Clean(root)), "/") {
		if strings.EqualFold(component, ".git") {
			return Snapshot{}, fail("INVALID_ROOT", "Внутренний каталог Git не является переносимым проектом.")
		}
	}
	clean, err := ValidateExcludes(excludes)
	if err != nil {
		return Snapshot{}, err
	}
	if limits.MaxMetadataBytes == 0 {
		limits.MaxMetadataBytes = defaultMetadataBytes
	}
	if limits.MaxFiles <= 0 || limits.MaxFiles > 100000 || limits.MaxFileBytes <= 0 || limits.MaxFileBytes > 64<<20 || limits.MaxTotalBytes <= 0 || limits.MaxTotalBytes > 512<<20 || limits.MaxMetadataBytes < metadataEnvelopeBytes || limits.MaxMetadataBytes > 32<<20 {
		return Snapshot{}, fail("INVALID_LIMITS", "Недопустимые ограничения снимка.")
	}
	result := Snapshot{Files: map[string][]byte{}, Root: root, Manifest: Manifest{Version: 1, Excludes: clean, Files: map[string]Metadata{}}}
	budget := scanBudget{limits: limits, metadataBytes: metadataEnvelopeBytes}
	tree, err := scanTreeWithOptions(ctx, root, &budget, clean, func(name string, metadata *Metadata, data []byte) error {
		result.Files[name] = data
		result.Manifest.Files[name] = *metadata
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	defer tree.root.Close()
	if len(tree.blocked) > 0 {
		return Snapshot{}, fail("INCOMPLETE_CAPTURE", "Снимок содержит неподдерживаемые ссылки или специальные файлы. Явно исключите их перед экспортом.")
	}
	if err := tree.verify(ctx); err != nil {
		return Snapshot{}, err
	}
	paths := make([]string, 0, len(result.Files))
	for name := range result.Files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	result.Manifest.Policy = ReadGitPolicy(ctx, root, paths)
	if err := tree.verify(ctx); err != nil {
		return Snapshot{}, err
	}
	result.Manifest.ID, err = ManifestID(result.Manifest)
	if err != nil {
		return Snapshot{}, err
	}
	return result, nil
}

// ManifestID hashes a deterministic JSON encoding of every field except ID.
// encoding/json sorts map keys; excludes must already be sorted and unique.
func ManifestID(manifest Manifest) (string, error) {
	manifest.ID = ""
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", fail("INVALID_MANIFEST", "Не удалось вычислить идентификатор снимка.")
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func ValidateSnapshot(snapshot Snapshot) error {
	manifest := snapshot.Manifest
	if manifest.Version != 1 || manifest.Files == nil || snapshot.Files == nil {
		return fail("INVALID_MANIFEST", "Неподдерживаемый или неполный снимок.")
	}
	if manifest.Policy.Supported {
		if manifest.Policy.Kind != "raw-v1" || manifest.Policy.Fingerprint != policyHash([]byte("raw-v1")) || manifest.Policy.Reason != "" {
			return fail("INVALID_MANIFEST", "Неизвестная поддержанная политика снимка.")
		}
	} else if manifest.Policy.Kind != "unknown" || manifest.Policy.Fingerprint != "" || len(manifest.Policy.Reason) > 512 {
		return fail("INVALID_MANIFEST", "Неизвестная политика снимка.")
	}
	excludes, err := ValidateExcludes(manifest.Excludes)
	if err != nil {
		return err
	}
	if len(excludes) != len(manifest.Excludes) {
		return fail("INVALID_MANIFEST", "Повторяющиеся исключения снимка.")
	}
	for i := range excludes {
		if excludes[i] != manifest.Excludes[i] {
			return fail("INVALID_MANIFEST", "Исключения снимка не упорядочены.")
		}
	}
	limits := DefaultLimits()
	if len(manifest.Files) > limits.MaxFiles || len(manifest.Files) != len(snapshot.Files) {
		return fail("INVALID_MANIFEST", "Недопустимый состав снимка.")
	}
	budget := scanBudget{limits: limits, metadataBytes: metadataEnvelopeBytes}
	total := int64(0)
	cases := map[string]bool{}
	for name, metadata := range manifest.Files {
		if err := ValidateRelativePath(name); err != nil {
			return err
		}
		if excludedRelative(name, excludes) || forbiddenPath(name) {
			return fail("EXCLUDED_PATH", "Снимок содержит исключённый путь.")
		}
		key := strings.ToLower(name)
		if cases[key] {
			return fail("PATH_CASE_COLLISION", "Снимок содержит неоднозначные пути Windows.")
		}
		cases[key] = true
		data, ok := snapshot.Files[name]
		if !ok || int64(len(data)) != metadata.Size || metadata.Size < 0 || metadata.Size > limits.MaxFileBytes {
			return fail("INVALID_MANIFEST", "Размер файла не совпадает с описанием снимка.")
		}
		total += metadata.Size
		if total > limits.MaxTotalBytes {
			return fail("TOTAL_SIZE_LIMIT", "Снимок превышает общий лимит размера.")
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != metadata.SHA256 {
			return fail("SNAPSHOT_HASH_MISMATCH", "Содержимое файла не совпадает с описанием снимка.")
		}
		actual := Metadata{}
		analyzeRepresentation(data, &actual)
		if actual.Encoding != metadata.Encoding || actual.BOM != metadata.BOM || actual.EOL != metadata.EOL || actual.FinalNewline != metadata.FinalNewline {
			return fail("INVALID_MANIFEST", "Представление файла не совпадает с содержимым.")
		}
		if metadata.Mode != "0444" && metadata.Mode != "0666" && metadata.Mode != "0600" && metadata.Mode != "0644" && metadata.Mode != "0755" && metadata.Mode != "0700" {
			return fail("UNSUPPORTED_MODE", "Режим файла не поддерживается.")
		}
		if err := budget.reserveEntryPath(".", name); err != nil {
			return err
		}
		if err := budget.reserveJSON(metadata, 1); err != nil {
			return err
		}
	}
	for name := range manifest.Files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if cases[strings.ToLower(parent)] {
				return fail("PATH_TYPE_CONFLICT", "Путь снимка одновременно является файлом и каталогом.")
			}
		}
	}
	spellings := map[string]string{}
	for name := range manifest.Files {
		for current := name; current != "."; current = path.Dir(current) {
			key := strings.ToLower(current)
			if previous, ok := spellings[key]; ok && previous != current {
				return fail("PATH_CASE_COLLISION", "Неоднозначный регистр каталогов снимка.")
			}
			spellings[key] = current
		}
	}
	id, err := ManifestID(manifest)
	if err != nil {
		return err
	}
	if id != manifest.ID {
		return fail("MANIFEST_HASH_MISMATCH", "Идентификатор снимка не совпадает с его содержимым.")
	}
	return nil
}

func ValidateExcludes(excludes []string) ([]string, error) {
	if len(excludes) > 1000 {
		return nil, fail("INVALID_EXCLUDES", "Слишком много исключений.")
	}
	result := append([]string{}, excludes...)
	seen := map[string]bool{}
	total := 0
	for _, name := range result {
		if err := ValidateRelativePath(name); err != nil {
			return nil, err
		}
		total += len(name)
		if total > 64<<10 {
			return nil, fail("INVALID_EXCLUDES", "Слишком длинный список исключений.")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fail("INVALID_EXCLUDES", "Исключения повторяются.")
		}
		seen[key] = true
	}
	sort.Strings(result)
	return result, nil
}

func ValidateRelativePath(name string) error {
	if name == "" || len(name) > 32760 || name != path.Clean(name) || strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return fail("INVALID_PATH", "Недопустимый относительный путь.")
	}
	if len(strings.Split(name, "/")) > 129 {
		return fail("INVALID_PATH", "Превышена глубина пути снимка.")
	}
	for _, part := range strings.Split(name, "/") {
		units := 0
		for _, r := range part {
			units++
			if r > 0xffff {
				units++
			}
		}
		if units > 255 {
			return fail("INVALID_PATH", "Слишком длинный компонент пути Windows.")
		}
		for _, r := range part {
			if r < 32 {
				return fail("INVALID_PATH", "Управляющие символы в пути запрещены.")
			}
		}
		if strings.ContainsAny(part, "<>\"|?*") || strings.TrimRight(part, " .") != part {
			return fail("INVALID_PATH", "Путь несовместим с Windows.")
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" || (len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9') {
			return fail("INVALID_PATH", "Зарезервированное имя Windows.")
		}
	}
	return nil
}
func excludedRelative(name string, excludes []string) bool {
	name = strings.ToLower(strings.ReplaceAll(name, "\\", "/"))
	for _, exclude := range excludes {
		exclude = strings.ToLower(exclude)
		if name == exclude || strings.HasPrefix(name, exclude+"/") {
			return true
		}
	}
	return false
}
func forbiddenPath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if excluded(part, false) {
			return true
		}
	}
	return false
}
