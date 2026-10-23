package compare

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureDeterministicRootsExcludesAndValidation(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, root := range []string{a, b} {
		write(t, root, "src/текст.txt", []byte("CAPTURE_PRIVATE_PHRASE\r\ntwo\r\n"))
		write(t, root, ".env", []byte("secret"))
		write(t, root, "copied-private.fmstate", []byte("protected identity must not be exported"))
		write(t, root, "skip/private.txt", []byte("private"))
	}
	first, err := Capture(context.Background(), a, []string{"skip"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Capture(context.Background(), b, []string{"skip"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.ID != second.Manifest.ID || len(first.Files) != 1 {
		t.Fatalf("nondeterministic/scoped capture: %+v %+v", first.Manifest, second.Manifest)
	}
	if err := ValidateSnapshot(first); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(first)
	if strings.Contains(string(data), "CAPTURE_PRIVATE_PHRASE") || strings.Contains(string(data), `"root":`) || strings.Contains(string(data), a) {
		t.Fatal("snapshot JSON leaks source or root")
	}
	first.Files["src/текст.txt"][0] = 'X'
	if err := ValidateSnapshot(first); ErrorCode(err) != "SNAPSHOT_HASH_MISMATCH" {
		t.Fatalf("corrupted payload accepted: %v", err)
	}
}

func TestCaptureExplicitExcludesAndLinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	write(t, root, "ok.txt", nil)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip(err)
	}
	if _, err := Capture(context.Background(), root, nil); ErrorCode(err) != "INCOMPLETE_CAPTURE" {
		t.Fatalf("partial capture accepted: %v", err)
	}
	if _, err := Capture(context.Background(), root, []string{"linked"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../outside", "C:/secret", ".git/../ok", "a\\b", "a:stream", "a.", "NUL.txt", "a\nfile"} {
		if _, err := ValidateExcludes([]string{bad}); err == nil {
			t.Fatalf("bad exclude %q accepted", bad)
		}
	}
}

func TestCaptureCannotUseGitMetadataAsProjectRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".git", "objects")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(context.Background(), root, nil); ErrorCode(err) != "INVALID_ROOT" {
		t.Fatalf("Git metadata root accepted: %v", err)
	}
}

func TestGitPolicyTransformationIsBlockedAndFilterNotExecuted(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git not installed")
	}
	root := t.TempDir()
	command := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, data)
		}
	}
	command("init", "--quiet")
	command("config", "core.autocrlf", "false")
	command("config", "core.eol", "lf")
	write(t, root, "file.txt", []byte("x\n"))
	snapshot, err := Capture(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Manifest.Policy.Supported {
		t.Fatalf("raw Git policy rejected: %+v", snapshot.Manifest.Policy)
	}
	command("config", "core.autocrlf", "true")
	snapshot, err = Capture(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest.Policy.Supported {
		t.Fatal("autocrlf transformation accepted")
	}
	command("config", "core.autocrlf", "false")
	command("config", "filter.bad.clean", "this-command-must-never-execute")
	write(t, root, ".gitattributes", []byte("*.txt filter=bad\n"))
	snapshot, err = Capture(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest.Policy.Supported {
		t.Fatal("filter accepted")
	}
}

func TestIndexModePolicyRejectsUnsupportedTypesStagesAndCase(t *testing.T) {
	hash := strings.Repeat("a", 40)
	for _, record := range []string{"100755 " + hash + " 0\tfile.txt\x00", "120000 " + hash + " 0\tfile.txt\x00", "100644 " + hash + " 2\tfile.txt\x00", "100644 " + hash + " 0\tfile.txt\x00100644 " + hash + " 0\tFILE.TXT\x00"} {
		if unsupportedIndexMode([]byte(record), []string{"file.txt"}) == "" {
			t.Fatal("unsupported index accepted")
		}
	}
	if unsupportedIndexMode([]byte("100644 "+hash+" 0\tfile.txt\x00"), []string{"file.txt"}) != "" {
		t.Fatal("raw index rejected")
	}
	if unsupportedIndexMode([]byte("160000 "+hash+" 0\tmodule\x00"), []string{"module/source.go"}) == "" {
		t.Fatal("nested submodule accepted")
	}
}

func TestGitExecutableIndexModeBlocksCaptureApplyPolicy(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git not installed")
	}
	root := t.TempDir()
	command := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", root, "-c", "core.hooksPath=NUL", "-c", "core.fsmonitor=false"}, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=NUL", "GIT_TERMINAL_PROMPT=0")
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, data)
		}
	}
	command("init", "--quiet")
	command("config", "core.autocrlf", "false")
	command("config", "core.eol", "lf")
	write(t, root, "script.sh", []byte("echo hello\n"))
	command("add", "--", "script.sh")
	command("update-index", "--chmod=+x", "--", "script.sh")
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Capture(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifest.Policy.Supported {
		t.Fatal("Git100755 silently represented as Windows mode")
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("policy probe changed index")
	}
}
