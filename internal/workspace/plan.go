// Package workspace plans and applies explicitly confirmed local changes.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strings"

	"figmobil/internal/compare"
)

type DeleteIntent struct {
	Path               string `json:"path"`
	ExpectedBaseSHA256 string `json:"expectedBaseSha256"`
}
type PlanItem struct {
	Path   string            `json:"path"`
	Status string            `json:"status"`
	Action string            `json:"action"`
	Reason string            `json:"reason"`
	Before *compare.Metadata `json:"before,omitempty"`
	After  *compare.Metadata `json:"after,omitempty"`
}
type ApplyPlan struct {
	ID         string     `json:"id"`
	BaseID     string     `json:"baseId"`
	LocalID    string     `json:"localId"`
	IncomingID string     `json:"incomingId"`
	CanApply   bool       `json:"canApply"`
	Items      []PlanItem `json:"items"`
	Root       string     `json:"-"`
	seal       [32]byte
	excludes   []string
}
type Error struct{ Code, Message string }

func (e *Error) Error() string        { return e.Message }
func fail(code, message string) error { return &Error{code, message} }

func Plan(base *compare.Manifest, local, incoming compare.Snapshot, deletes []DeleteIntent) (ApplyPlan, error) {
	return makePlan(base, local, incoming, deletes, false)
}
func PlanInitial(local, incoming compare.Snapshot) (ApplyPlan, error) {
	return makePlan(nil, local, incoming, nil, true)
}

func makePlan(base *compare.Manifest, local, incoming compare.Snapshot, deletes []DeleteIntent, initial bool) (ApplyPlan, error) {
	if err := compare.ValidateSnapshot(local); err != nil {
		return ApplyPlan{}, err
	}
	if err := compare.ValidateSnapshot(incoming); err != nil {
		return ApplyPlan{}, err
	}
	if local.Root == "" {
		return ApplyPlan{}, fail("INVALID_ROOT", "Для применения требуется привязка локальной папки.")
	}
	if base != nil {
		if base.Version != 1 || base.Files == nil {
			return ApplyPlan{}, fail("INVALID_BASE", "Неподдерживаемая общая база.")
		}
		id, err := compare.ManifestID(*base)
		if err != nil || id != base.ID {
			return ApplyPlan{}, fail("INVALID_BASE", "Идентификатор общей базы не совпадает с содержимым.")
		}
	}
	plan := ApplyPlan{LocalID: local.Manifest.ID, IncomingID: incoming.Manifest.ID, CanApply: true, Items: []PlanItem{}, Root: local.Root, excludes: append([]string{}, local.Manifest.Excludes...)}
	if base != nil {
		plan.BaseID = base.ID
	}
	blockedReason := ""
	if base == nil && !initial {
		blockedReason = "Нет подтверждённой общей базы."
	}
	if !local.Manifest.Policy.Supported || !incoming.Manifest.Policy.Supported {
		blockedReason = "Политика рабочей копии не поддерживается для применения."
	}
	if local.Manifest.Policy.Fingerprint != incoming.Manifest.Policy.Fingerprint {
		blockedReason = "Политики рабочей копии различаются."
	}
	if !sameStrings(local.Manifest.Excludes, incoming.Manifest.Excludes) || (base != nil && !sameStrings(base.Excludes, local.Manifest.Excludes)) {
		blockedReason = "Области исключений различаются; требуется отдельное согласование."
	}
	deleted := map[string]DeleteIntent{}
	for _, intent := range deletes {
		if err := compare.ValidateRelativePath(intent.Path); err != nil {
			return ApplyPlan{}, err
		}
		if _, exists := deleted[intent.Path]; exists {
			return ApplyPlan{}, fail("INVALID_DELETE", "Повторяющееся удаление.")
		}
		if _, exists := incoming.Manifest.Files[intent.Path]; exists {
			return ApplyPlan{}, fail("INVALID_DELETE", "Пакет одновременно содержит файл и его удаление.")
		}
		if base == nil {
			return ApplyPlan{}, fail("INVALID_DELETE", "Удаление требует подтверждённой базы.")
		}
		metadata, exists := base.Files[intent.Path]
		if !exists || metadata.SHA256 != intent.ExpectedBaseSHA256 {
			return ApplyPlan{}, fail("INVALID_DELETE", "Удаление не соответствует общей базе.")
		}
		deleted[intent.Path] = intent
	}
	paths := map[string]bool{}
	for name := range local.Manifest.Files {
		paths[name] = true
	}
	for name := range incoming.Manifest.Files {
		paths[name] = true
	}
	for name := range deleted {
		paths[name] = true
	}
	ordered := make([]string, 0, len(paths))
	for name := range paths {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		before, beforeOK := local.Manifest.Files[name]
		after, afterOK := incoming.Manifest.Files[name]
		var old compare.Metadata
		oldOK := false
		if base != nil {
			old, oldOK = base.Files[name]
		}
		item := PlanItem{Path: name, Action: "skip", Status: "same"}
		if beforeOK {
			copy := before
			item.Before = &copy
		}
		if afterOK {
			copy := after
			item.After = &copy
		}
		_, isDelete := deleted[name]
		switch {
		case isDelete && !beforeOK:
			item.Status = "same"
			item.Reason = "Файл уже отсутствует."
		case isDelete && equalMetadata(before, old):
			item.Status = "incoming-change"
			item.Action = "delete"
			item.Reason = "Явное удаление совпадает с общей базой."
		case isDelete:
			item.Status = "conflict"
			item.Reason = "Удаление пересекается с локальной правкой."
		case !afterOK:
			item.Status = "local-only"
			item.Reason = "Отсутствие во входящем снимке не означает удаление."
		case beforeOK && equalMetadata(before, after):
			item.Reason = "Байты и режим уже совпадают."
		case initial && !beforeOK:
			item.Status = "incoming-change"
			item.Action = "create"
			item.Reason = "Начальный импорт нового файла."
		case initial:
			item.Status = "conflict"
			item.Reason = "Существующая версия отличается; начальный импорт её не заменяет."
		case base == nil:
			item.Status = "needs-review"
			item.Reason = "Нет подтверждённой общей базы."
		case beforeOK == oldOK && (!beforeOK || equalMetadata(before, old)):
			item.Status = "incoming-change"
			if beforeOK {
				item.Action = "replace"
			} else {
				item.Action = "create"
			}
			item.Reason = "Входящая правка применяется к неизменённой локальной базе."
		case afterOK == oldOK && (!afterOK || equalMetadata(after, old)):
			item.Status = "local-change"
			item.Reason = "Локальная работа сохранена."
		default:
			item.Status = "conflict"
			item.Reason = "Обе версии отличаются от базы."
		}
		if item.Action != "skip" && (strings.EqualFold(name, ".gitattributes") || strings.HasSuffix(strings.ToLower(name), "/.gitattributes")) {
			item.Status = "conflict"
			item.Action = "skip"
			item.Reason = "Изменение правил checkout требует отдельной приёмки."
		}
		if item.Action == "replace" && (before.Encoding != after.Encoding || before.BOM != after.BOM || before.EOL != after.EOL) {
			item.Reason = "Входящая правка меняет представление файла. Применяются точные исходные байты без нормализации."
		}
		if item.Action == "create" && after.Mode != "0666" {
			item.Status = "conflict"
			item.Action = "skip"
			item.Reason = "Создание неподдерживаемого режима файла запрещено."
		}
		if (item.Action == "replace" || item.Action == "delete") && before.Mode != "0666" {
			item.Status = "conflict"
			item.Action = "skip"
			item.Reason = "Запись или удаление файла с этим режимом пока не поддерживается."
		}
		if beforeOK && afterOK && before.Mode != after.Mode {
			item.Status = "conflict"
			item.Action = "skip"
			item.Reason = "Перенос режима файла пока не поддерживается."
		}
		if item.Status == "conflict" || item.Status == "needs-review" {
			plan.CanApply = false
		}
		plan.Items = append(plan.Items, item)
	}
	// Check cross-snapshot directory and Windows case collisions, including paths
	// absent from the other manifest. Do not mistake them for independent adds.
	byPath := map[string]int{}
	markConflict := func(index int) {
		plan.CanApply = false
		plan.Items[index].Status = "conflict"
		plan.Items[index].Action = "skip"
		plan.Items[index].Reason = "Неоднозначный регистр или конфликт файла и каталога."
	}
	for i, item := range plan.Items {
		key := strings.ToLower(item.Path)
		if previous, ok := byPath[key]; ok {
			markConflict(previous)
			markConflict(i)
		} else {
			byPath[key] = i
		}
	}
	spelling := map[string]string{}
	ambiguousDirs := map[string]bool{}
	for _, item := range plan.Items {
		for parent := path.Dir(item.Path); parent != "."; parent = path.Dir(parent) {
			key := strings.ToLower(parent)
			if previous, ok := spelling[key]; ok && previous != parent {
				ambiguousDirs[key] = true
			} else {
				spelling[key] = parent
			}
		}
	}
	for i, item := range plan.Items {
		for parent := path.Dir(strings.ToLower(item.Path)); parent != "."; parent = path.Dir(parent) {
			if ambiguousDirs[parent] {
				markConflict(i)
			}
		}
	}
	for i, item := range plan.Items {
		for parent := path.Dir(strings.ToLower(item.Path)); parent != "."; parent = path.Dir(parent) {
			if previous, ok := byPath[parent]; ok {
				markConflict(previous)
				markConflict(i)
			}
		}
	}
	if blockedReason != "" {
		plan.CanApply = false
		for i := range plan.Items {
			if plan.Items[i].Action != "skip" {
				plan.Items[i].Action = "skip"
				plan.Items[i].Status = "needs-review"
				plan.Items[i].Reason = blockedReason
			}
		}
	}
	plan.ID = planID(plan)
	plan.seal = sealPlan(plan)
	return plan, nil
}

func equalMetadata(a, b compare.Metadata) bool {
	return a.Size == b.Size && a.SHA256 == b.SHA256 && a.Mode == b.Mode
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func planID(plan ApplyPlan) string {
	plan.ID = ""
	data, _ := json.Marshal(plan)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func sealPlan(plan ApplyPlan) [32]byte {
	data, _ := json.Marshal(plan)
	data = append(data, []byte("\x00"+plan.Root+"\x00"+strings.Join(plan.excludes, "\x00"))...)
	return sha256.Sum256(data)
}
