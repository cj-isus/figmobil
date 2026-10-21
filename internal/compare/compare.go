// Package compare reads selected local directories without changing their files.
// A report describes bytes observed during this operation, not a Git merge plan.
package compare

import (
	"context"
	"errors"
	"sort"
	"strings"
)

type Request struct {
	BaseRoot     string `json:"baseRoot"`
	LocalRoot    string `json:"localRoot"`
	IncomingRoot string `json:"incomingRoot"`
}

type Limits struct {
	MaxFiles         int   `json:"maxFiles"`
	MaxFileBytes     int64 `json:"maxFileBytes"`
	MaxTotalBytes    int64 `json:"maxTotalBytes"`
	MaxMetadataBytes int64 `json:"maxMetadataBytes"`
}

const defaultMetadataBytes int64 = 8 << 20

func DefaultLimits() Limits {
	return Limits{MaxFiles: 5000, MaxFileBytes: 8 << 20, MaxTotalBytes: 32 << 20, MaxMetadataBytes: defaultMetadataBytes}
}

type Metadata struct {
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Encoding     string `json:"encoding"`
	BOM          string `json:"bom"`
	EOL          string `json:"eol"`
	FinalNewline bool   `json:"finalNewline"`
	Mode         string `json:"mode"`
}

type Item struct {
	Path                  string    `json:"path"`
	Status                string    `json:"status"`
	Reason                string    `json:"reason"`
	Base                  *Metadata `json:"base,omitempty"`
	Local                 *Metadata `json:"local,omitempty"`
	Incoming              *Metadata `json:"incoming,omitempty"`
	RepresentationChanged bool      `json:"representationChanged"`
}

type Warning struct {
	Code    string `json:"code"`
	Root    string `json:"root"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Scan struct {
	Files    int   `json:"files"`
	Bytes    int64 `json:"bytes"`
	Excluded int   `json:"excluded"`
	Skipped  int   `json:"skipped"`
}

type Report struct {
	Items    []Item          `json:"items"`
	Summary  map[string]int  `json:"summary"`
	Scans    map[string]Scan `json:"scans"`
	Complete bool            `json:"complete"`
	Warnings []Warning       `json:"warnings"`
	HasBase  bool            `json:"hasBase"`
}

// Error contains a stable code and safe message; it never contains file content.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }
func ErrorCode(err error) string {
	var domain *Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	return "COMPARISON_FAILED"
}
func fail(code, message string) error { return &Error{code, message} }

// Run scans every selected root. A failed scan returns no partial comparison.
// Deliberately excluded paths are outside scope; skipped links are explicitly
// reported and paths beneath them cannot be classified as present or absent.
func Run(ctx context.Context, req Request, limits Limits) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	// Omitted metadata limits keep existing keyed callers on a bounded default.
	if limits.MaxMetadataBytes == 0 {
		limits.MaxMetadataBytes = defaultMetadataBytes
	}
	if limits.MaxFiles <= 0 || limits.MaxFiles > 100000 || limits.MaxFileBytes <= 0 || limits.MaxFileBytes > 64<<20 || limits.MaxTotalBytes <= 0 || limits.MaxTotalBytes > 512<<20 || limits.MaxMetadataBytes < metadataEnvelopeBytes || limits.MaxMetadataBytes > 32<<20 {
		return Report{}, fail("INVALID_LIMITS", "Недопустимые ограничения сравнения.")
	}
	if req.LocalRoot == "" || req.IncomingRoot == "" {
		return Report{}, fail("INVALID_ROOT", "Выберите локальную и входящую папки.")
	}
	report := Report{Items: []Item{}, Summary: map[string]int{}, Scans: map[string]Scan{}, Complete: true, Warnings: []Warning{}, HasBase: req.BaseRoot != ""}
	budget := scanBudget{limits: limits, metadataBytes: metadataEnvelopeBytes}
	trees := map[string]*snapshot{}
	defer func() {
		for _, tree := range trees {
			_ = tree.root.Close()
		}
	}()
	roots := []struct{ name, path string }{{"local", req.LocalRoot}, {"incoming", req.IncomingRoot}}
	if report.HasBase {
		roots = append(roots, struct{ name, path string }{"base", req.BaseRoot})
	}
	for _, root := range roots {
		tree, err := scanTree(ctx, root.path, &budget)
		if err != nil {
			return Report{}, err
		}
		trees[root.name] = tree
		report.Scans[root.name] = tree.scan
		for _, warning := range tree.warnings {
			warning.Root = root.name
			report.Warnings = append(report.Warnings, warning)
		}
		if len(tree.blocked) > 0 {
			report.Complete = false
		}
	}
	// Recheck observations after all roots have been read, so a change while a
	// later root was scanned does not silently certify an earlier observation.
	for _, root := range roots {
		if err := trees[root.name].verify(ctx); err != nil {
			return Report{}, err
		}
	}
	paths := map[string]bool{}
	for _, tree := range trees {
		for path := range tree.files {
			paths[path] = true
		}
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	structural, err := structuralConflicts(trees, paths, &report, &budget)
	if err != nil {
		return Report{}, err
	}
	conflictPrefixes := make([]string, 0, len(structural))
	for prefix := range structural {
		conflictPrefixes = append(conflictPrefixes, prefix)
	}
	sort.Slice(conflictPrefixes, func(i, j int) bool {
		if len(conflictPrefixes[i]) != len(conflictPrefixes[j]) {
			return len(conflictPrefixes[i]) > len(conflictPrefixes[j])
		}
		return conflictPrefixes[i] < conflictPrefixes[j]
	})
	for _, path := range ordered {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		blocked := false
		for _, tree := range trees {
			if tree.isBlocked(path) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		item := Item{Path: path, Local: trees["local"].files[path], Incoming: trees["incoming"].files[path]}
		if report.HasBase {
			item.Base = trees["base"].files[path]
		}
		item.Status, item.Reason = classify(item.Base, item.Local, item.Incoming, report.HasBase)
		for _, prefix := range conflictPrefixes {
			key := strings.ToLower(path)
			if key == prefix || strings.HasPrefix(key, prefix+"/") {
				item.Status, item.Reason = "conflict", structural[prefix]
				break
			}
		}
		item.RepresentationChanged = representationChanged(item.Local, item.Incoming)
		if err := budget.reserveJSON(item, 1); err != nil {
			return Report{}, err
		}
		report.Items = append(report.Items, item)
		report.Summary[item.Status]++
	}
	return report, nil
}

// Windows paths are case insensitive by default. Ambiguous spellings and
// directory/file collisions are surfaced before individual content statuses.
func structuralConflicts(trees map[string]*snapshot, files map[string]bool, report *Report, budget *scanBudget) (map[string]string, error) {
	variants := map[string]map[string]bool{}
	directories := map[string]bool{}
	for _, tree := range trees {
		for path := range tree.dirs {
			directories[strings.ToLower(path)] = true
		}
	}
	add := func(path string) {
		key := strings.ToLower(path)
		if variants[key] == nil {
			variants[key] = map[string]bool{}
		}
		variants[key][path] = true
	}
	for path := range files {
		add(path)
	}
	for _, tree := range trees {
		for path := range tree.dirs {
			add(path)
		}
	}
	conflicts := map[string]string{}
	keys := make([]string, 0, len(variants))
	for key := range variants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if len(variants[key]) > 1 {
			conflicts[key] = "Пути отличаются только регистром; требуется явная проверка совместимости Windows."
			if err := budget.appendWarning(&report.Warnings, Warning{Code: "PATH_CASE_COLLISION", Root: "comparison", Path: key, Message: conflicts[key]}); err != nil {
				return nil, err
			}
		}
	}
	for _, path := range sortedPaths(files) {
		key := strings.ToLower(path)
		if directories[key] {
			conflicts[key] = "Один и тот же путь является файлом и каталогом в разных копиях."
			if err := budget.appendWarning(&report.Warnings, Warning{Code: "PATH_TYPE_CONFLICT", Root: "comparison", Path: path, Message: conflicts[key]}); err != nil {
				return nil, err
			}
		}
	}
	return conflicts, nil
}

func sortedPaths(paths map[string]bool) []string {
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func equal(a, b *Metadata) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Size == b.Size && a.SHA256 == b.SHA256 && a.Mode == b.Mode
}
func classify(base, local, incoming *Metadata, hasBase bool) (string, string) {
	if equal(local, incoming) {
		if local == nil {
			return "same", "Файл отсутствует с обеих сторон; это не команда удаления."
		}
		return "same", "Локальная и входящая версии совпадают."
	}
	if !hasBase {
		if local == nil {
			return "added", "Файл есть только во входящей папке; общая база не задана."
		}
		if incoming == nil {
			return "local-only", "Файл есть только локально; отсутствие во входящей папке не означает удаление."
		}
		return "needs-review", "Версии различаются; без подтверждённой общей базы требуется проверка."
	}
	if equal(local, base) {
		if incoming == nil {
			return "incoming-missing", "Файл отсутствует во входящей папке; это не команда удаления."
		}
		return "incoming-change", "Входящая версия отличается от базы; локальная версия совпадает с базой."
	}
	if equal(incoming, base) {
		return "local-change", "Локальная версия отличается от базы; входящая версия совпадает с базой."
	}
	return "conflict", "Обе стороны отличаются от базы; автоматическое применение не выполняется."
}
func representationChanged(a, b *Metadata) bool {
	return a != nil && b != nil && (a.Encoding != b.Encoding || a.BOM != b.BOM || a.EOL != b.EOL || a.FinalNewline != b.FinalNewline || a.Mode != b.Mode)
}
