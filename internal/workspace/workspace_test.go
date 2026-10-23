package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"figmobil/internal/compare"
)

func fixture(t *testing.T, files map[string]string) compare.Snapshot {
	t.Helper()
	root := t.TempDir()
	for name, data := range files {
		put(t, root, name, data)
	}
	return capture(t, root)
}
func capture(t *testing.T, root string) compare.Snapshot {
	t.Helper()
	snapshot, err := compare.Capture(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Manifest.Policy.Supported {
		t.Fatalf("fixture unsupported policy: %+v", snapshot.Manifest.Policy)
	}
	return snapshot
}
func put(t *testing.T, root, name, data string) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func contents(t *testing.T, root, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func code(err error) string {
	var domain *Error
	if errors.As(err, &domain) {
		return domain.Code
	}
	return compare.ErrorCode(err)
}

func TestThreeWayPlanPreservesLocalWorkAndBlocksConflicts(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "one\ntwo\n", "local.txt": "base\n"})
	local := fixture(t, map[string]string{"a.txt": "one\ntwo\n", "local.txt": "local\n", "extra.txt": "extra\n"})
	incoming := fixture(t, map[string]string{"a.txt": "one\nnew\n", "local.txt": "base\n"})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatal(plan)
	}
	statuses := map[string]PlanItem{}
	for _, item := range plan.Items {
		statuses[item.Path] = item
	}
	if statuses["a.txt"].Action != "replace" || statuses["local.txt"].Status != "local-change" || statuses["extra.txt"].Action != "skip" {
		t.Fatalf("unexpected plan %+v", plan)
	}
	put(t, local.Root, "a.txt", "changed locally\n")
	local = capture(t, local.Root)
	plan, err = Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply {
		t.Fatal("conflicting plan allowed")
	}
}

func TestInitialPlanOnlyCreatesMissingFiles(t *testing.T) {
	local := fixture(t, map[string]string{"same.txt": "same\n", "local.txt": "local\n"})
	incoming := fixture(t, map[string]string{"same.txt": "same\n", "new/file.txt": "new\n"})
	plan, err := PlanInitial(local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatal(plan)
	}
	unknown, err := Plan(nil, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.CanApply {
		t.Fatal("unknown base silently accepted")
	}
	put(t, incoming.Root, "same.txt", "different\n")
	incoming = capture(t, incoming.Root)
	plan, err = PlanInitial(local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CanApply {
		t.Fatal("initial overwrite accepted")
	}
}

func TestExplicitDeletionRequiresExpectedBaseAndUnchangedLocal(t *testing.T) {
	base := fixture(t, map[string]string{"delete.txt": "old\n"})
	local := fixture(t, map[string]string{"delete.txt": "old\n"})
	incoming := fixture(t, map[string]string{})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Items[0].Action != "skip" {
		t.Fatal("absence treated as delete")
	}
	intent := DeleteIntent{Path: "delete.txt", ExpectedBaseSHA256: base.Manifest.Files["delete.txt"].SHA256}
	plan, err = Plan(&base.Manifest, local, incoming, []DeleteIntent{intent})
	if err != nil || !plan.CanApply || plan.Items[0].Action != "delete" {
		t.Fatalf("delete plan: %+v %v", plan, err)
	}
	intent.ExpectedBaseSHA256 = "wrong"
	if _, err := Plan(&base.Manifest, local, incoming, []DeleteIntent{intent}); code(err) != "INVALID_DELETE" {
		t.Fatalf("bad deletion accepted: %v", err)
	}
}
