//go:build windows

package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPreservesBytesUntouchedFilesAndCreatesNestedPaths(t *testing.T) {
	local := fixture(t, map[string]string{"same.txt": "same\r\n", "extra.txt": "local only\n"})
	incoming := fixture(t, map[string]string{"same.txt": "same\r\n", "src/new.txt": "new\r\n", "src/empty.txt": ""})
	before, _ := os.Stat(filepath.Join(local.Root, "same.txt"))
	plan, err := PlanInitial(local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(context.Background(), plan, incoming, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.RecoveryRequired {
		t.Fatal(result)
	}
	if contents(t, local.Root, "src/new.txt") != "new\r\n" || contents(t, local.Root, "extra.txt") != "local only\n" || contents(t, local.Root, "src/empty.txt") != "" {
		t.Fatal("unexpected bytes")
	}
	after, _ := os.Stat(filepath.Join(local.Root, "same.txt"))
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("untouched file rewritten")
	}
}

func TestApplyCancellationAndAncestorReplacementAreBlocked(t *testing.T) {
	local := fixture(t, map[string]string{})
	incoming := fixture(t, map[string]string{"new/a.txt": "new\n"})
	plan, err := PlanInitial(local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Apply(ctx, plan, incoming, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	result, err := apply(context.Background(), plan, incoming, t.TempDir(), func(string) error {
		if err := os.Rename(local.Root, local.Root+"-moved"); err == nil {
			return errors.New("locked ancestor was renamed")
		}
		return nil
	})
	if err != nil || result.Status != "applied" {
		t.Fatalf("ancestor exclusion: %+v %v", result, err)
	}
}

func TestPlainGitDiffIndexAndUnrelatedStagedWorkArePreserved(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	command := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(git, append([]string{"--no-pager", "--no-optional-locks", "-C", root, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=NUL"}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("Git fixture/check failed: %v %s", err, data)
		}
		return data
	}
	command("init", "--quiet")
	command("config", "user.name", "Synthetic Test")
	command("config", "user.email", "synthetic@example.invalid")
	command("config", "core.autocrlf", "false")
	command("config", "core.eol", "lf")
	put(t, root, "change.txt", "one\ntwo\n")
	put(t, root, "local.txt", "base\n")
	command("add", "--", "change.txt", "local.txt")
	command("commit", "--quiet", "-m", "synthetic base")
	base := capture(t, root)
	put(t, root, "local.txt", "staged local\n")
	command("add", "--", "local.txt")
	put(t, root, "local.txt", "unstaged local\n")
	local := capture(t, root)
	incoming := fixture(t, map[string]string{"change.txt": "one\nNEW\n", "local.txt": "base\n"})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanApply {
		t.Fatalf("plan blocked %+v", plan)
	}
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	stagedBefore := command("diff", "--cached", "--no-ext-diff", "--no-textconv", "--binary")
	localDiffBefore := command("diff", "--no-ext-diff", "--no-textconv", "--", "local.txt")
	result, err := Apply(context.Background(), plan, incoming, t.TempDir())
	if err != nil || result.Status != "applied" {
		t.Fatalf("apply: %+v %v", result, err)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	configAfter, _ := os.ReadFile(filepath.Join(root, ".git", "config"))
	if !bytes.Equal(indexBefore, indexAfter) || !bytes.Equal(configBefore, configAfter) {
		t.Fatal("index/config changed")
	}
	if !bytes.Equal(stagedBefore, command("diff", "--cached", "--no-ext-diff", "--no-textconv", "--binary")) || !bytes.Equal(localDiffBefore, command("diff", "--no-ext-diff", "--no-textconv", "--", "local.txt")) {
		t.Fatal("staged/unstaged local work changed")
	}
	diff := string(command("diff", "--no-ext-diff", "--no-textconv", "--", "change.txt"))
	if !strings.Contains(diff, "-two\n+NEW\n") || strings.Count(diff, "@@") != 2 || contents(t, root, "change.txt") != "one\nNEW\n" {
		t.Fatalf("unexpected plain diff: %s", diff)
	}
}

func TestApplyReplaceDeleteAndNoChange(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "old\n", "delete.txt": "remove\n"})
	local := fixture(t, map[string]string{"a.txt": "old\n", "delete.txt": "remove\n"})
	incoming := fixture(t, map[string]string{"a.txt": "new\n"})
	plan, err := Plan(&base.Manifest, local, incoming, []DeleteIntent{{Path: "delete.txt", ExpectedBaseSHA256: base.Manifest.Files["delete.txt"].SHA256}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(context.Background(), plan, incoming, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || contents(t, local.Root, "a.txt") != "new\n" {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(local.Root, "delete.txt")); !os.IsNotExist(err) {
		t.Fatalf("delete not complete: %v", err)
	}
	local = capture(t, local.Root)
	plan, err = Plan(&incoming.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = Apply(context.Background(), plan, incoming, t.TempDir())
	if err != nil || result.Status != "no-change" {
		t.Fatalf("no-change: %+v %v", result, err)
	}
}

func TestStaleOrTamperedPlanNeverWrites(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "old\n"})
	local := fixture(t, map[string]string{"a.txt": "old\n"})
	incoming := fixture(t, map[string]string{"a.txt": "new\n"})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.Items[0].Path = "elsewhere.txt"
	if _, err := Apply(context.Background(), plan, incoming, t.TempDir()); code(err) != "INVALID_PLAN" {
		t.Fatalf("tampered plan accepted: %v", err)
	}
	plan, err = Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	put(t, local.Root, "a.txt", "new local edit\n")
	if _, err := Apply(context.Background(), plan, incoming, t.TempDir()); code(err) != "SOURCE_CHANGED" {
		t.Fatalf("stale plan accepted: %v", err)
	}
	if contents(t, local.Root, "a.txt") != "new local edit\n" {
		t.Fatal("local edit lost")
	}
}

func TestHeldWriterBlocksApplyAndApplyBlocksConcurrentWriter(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "old\n"})
	local := fixture(t, map[string]string{"a.txt": "old\n"})
	incoming := fixture(t, map[string]string{"a.txt": "new\n"})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.OpenFile(filepath.Join(local.Root, "a.txt"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, applyErr := Apply(context.Background(), plan, incoming, t.TempDir())
	writer.Close()
	if code(applyErr) != "FILE_BUSY" {
		t.Fatalf("open writer not detected: %v", applyErr)
	}
	result, err := apply(context.Background(), plan, incoming, t.TempDir(), func(name string) error {
		if err := os.WriteFile(filepath.Join(local.Root, name), []byte("concurrent\n"), 0600); err == nil {
			return errors.New("concurrent write was allowed")
		}
		return nil
	})
	if err != nil || result.Status != "applied" {
		t.Fatalf("exclusive apply: %+v %v", result, err)
	}
}

func TestHardLinkedTargetNeverChangesOtherPath(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "old\n"})
	local := fixture(t, map[string]string{"a.txt": "old\n"})
	incoming := fixture(t, map[string]string{"a.txt": "new\n"})
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.Link(filepath.Join(local.Root, "a.txt"), outside); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), plan, incoming, t.TempDir()); code(err) != "UNSUPPORTED_LINK" {
		t.Fatalf("hard link accepted: %v", err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "old\n" || contents(t, local.Root, "a.txt") != "old\n" {
		t.Fatal("hard linked data changed")
	}
}

func TestConcurrentHardLinkKeepsOriginalStreamDuringReplacement(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "old\n"})
	local := fixture(t, map[string]string{"a.txt": "old\n"})
	incoming := fixture(t, map[string]string{"a.txt": "new\n"})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	result, err := apply(context.Background(), plan, incoming, t.TempDir(), func(name string) error {
		return os.Link(filepath.Join(local.Root, name), outside)
	})
	if err != nil || result.Status != "applied" {
		t.Fatalf("hard link race: %+v %v", result, err)
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "old\n" || contents(t, local.Root, "a.txt") != "new\n" {
		t.Fatalf("hardlink stream changed: %q %v", data, err)
	}
}

func TestFailureRollsBackAndJournalIsProtected(t *testing.T) {
	base := fixture(t, map[string]string{"a.txt": "PRIVATE_BACKUP_ORIGINAL\n", "b.txt": "old b\n"})
	local := fixture(t, map[string]string{"a.txt": "PRIVATE_BACKUP_ORIGINAL\n", "b.txt": "old b\n"})
	incoming := fixture(t, map[string]string{"a.txt": "PRIVATE_INCOMING_CONTENT\n", "b.txt": "new b\n"})
	plan, err := Plan(&base.Manifest, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	journalDir := t.TempDir()
	result, err := apply(context.Background(), plan, incoming, journalDir, func(name string) error {
		if name == "b.txt" {
			return errors.New("injected failure")
		}
		return nil
	})
	if err == nil || result.Status != "rolled-back" || !result.RecoveryRequired {
		t.Fatalf("rollback: %+v %v", result, err)
	}
	if contents(t, local.Root, "a.txt") != "PRIVATE_BACKUP_ORIGINAL\n" || contents(t, local.Root, "b.txt") != "old b\n" {
		t.Fatal("rollback lost originals")
	}
	entries, err := os.ReadDir(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(journalDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("PRIVATE_")) {
			t.Fatal("plaintext backup/journal")
		}
	}
	result, err = Recover(context.Background(), filepath.Join(journalDir, "journal.json"))
	if err != nil || result.RecoveryRequired {
		t.Fatalf("explicit cleanup: %+v %v", result, err)
	}
}

func simulateCrash(t *testing.T, localRoot string, plan ApplyPlan, incomingRoot string) string {
	t.Helper()
	incoming := capture(t, incomingRoot)
	journalDir := t.TempDir()
	panicked := false
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		_, _ = apply(context.Background(), plan, incoming, journalDir, func(name string) error {
			if name == "b.txt" {
				panic("simulated process interruption")
			}
			return nil
		})
	}()
	if !panicked || contents(t, localRoot, "a.txt") != "new a\n" {
		t.Fatal("crash fixture did not interrupt after first write")
	}
	return filepath.Join(journalDir, "journal.json")
}

func TestExplicitRecoveryRestoresKnownVersionsAndRejectsLaterEdit(t *testing.T) {
	for _, laterEdit := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "preserve-later-edit"}[laterEdit], func(t *testing.T) {
			base := fixture(t, map[string]string{"a.txt": "old a\n", "b.txt": "old b\n"})
			local := fixture(t, map[string]string{"a.txt": "old a\n", "b.txt": "old b\n"})
			incoming := fixture(t, map[string]string{"a.txt": "new a\n", "b.txt": "new b\n"})
			plan, err := Plan(&base.Manifest, local, incoming, nil)
			if err != nil {
				t.Fatal(err)
			}
			marker := simulateCrash(t, local.Root, plan, incoming.Root)
			if laterEdit {
				put(t, local.Root, "b.txt", "later user edit\n")
			}
			result, err := Recover(context.Background(), marker)
			if laterEdit {
				if code(err) != "RECOVERY_CONFLICT" || !result.RecoveryRequired || contents(t, local.Root, "a.txt") != "new a\n" || contents(t, local.Root, "b.txt") != "later user edit\n" {
					t.Fatalf("unsafe recovery: %+v %v", result, err)
				}
			} else if err != nil || result.RecoveryRequired || contents(t, local.Root, "a.txt") != "old a\n" {
				t.Fatalf("recovery: %+v %v", result, err)
			}
		})
	}
}
