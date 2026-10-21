package compare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func roots(t *testing.T) Request {
	t.Helper()
	return Request{BaseRoot: t.TempDir(), LocalRoot: t.TempDir(), IncomingRoot: t.TempDir()}
}

func write(t *testing.T, root, path string, data []byte) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, req Request) Report {
	t.Helper()
	report, err := Run(context.Background(), req, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestVersionMatrix(t *testing.T) {
	cases := []struct {
		name, base, local, incoming, status string
		hasBase                             bool
	}{
		{"equal-no-base", "", "a", "a", "same", false},
		{"added-no-base", "", "", "a", "added", false},
		{"local-no-base", "", "a", "", "local-only", false},
		{"different-no-base", "", "a", "b", "needs-review", false},
		{"unchanged", "a", "a", "a", "same", true},
		{"same-change", "a", "b", "b", "same", true},
		{"incoming-change", "a", "a", "b", "incoming-change", true},
		{"local-change", "a", "b", "a", "local-change", true},
		{"conflict", "a", "b", "c", "conflict", true},
		{"incoming-missing", "a", "a", "", "incoming-missing", true},
		{"local-missing", "a", "", "a", "local-change", true},
		{"both-missing", "a", "", "", "same", true},
		{"base-missing-incoming-add", "", "", "a", "incoming-change", true},
		{"base-missing-local-add", "", "a", "", "local-change", true},
		{"base-missing-conflict", "", "a", "b", "conflict", true},
		{"missing-versus-modify", "a", "", "b", "conflict", true},
		{"modify-versus-missing", "a", "b", "", "conflict", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			req := roots(t)
			for root, value := range map[string]string{req.BaseRoot: test.base, req.LocalRoot: test.local, req.IncomingRoot: test.incoming} {
				if value != "" {
					write(t, root, "source.txt", []byte(value))
				}
			}
			if !test.hasBase {
				req.BaseRoot = ""
			}
			report := run(t, req)
			if len(report.Items) != 1 || report.Items[0].Status != test.status {
				t.Fatalf("got %+v; want %s", report.Items, test.status)
			}
			if !report.Complete || report.HasBase != test.hasBase {
				t.Fatalf("unexpected report completeness/base: %+v", report)
			}
			if report.Summary[test.status] != 1 {
				t.Fatalf("summary: %+v", report.Summary)
			}
		})
	}
}

func TestByteRepresentationsAndNoWrites(t *testing.T) {
	req := roots(t)
	fixtures := map[string][]byte{
		"обычный.txt": []byte("привет\nстрока\n"), "crlf.txt": []byte("a\r\nb\r\n"),
		"mixed.txt": []byte("a\r\nb\nc\r"), "no-final.txt": []byte("a"), "empty.txt": {},
		"bom.txt":   append([]byte{0xef, 0xbb, 0xbf}, []byte("a\n")...),
		"utf16.txt": {0xff, 0xfe, 'a', 0, '\r', 0, '\n', 0}, "binary.bin": {0, 1, 2, 255},
		"legacy.txt": {0xe0, 0xe1, 0x0a},
	}
	before := map[string]os.FileInfo{}
	for name, data := range fixtures {
		for _, root := range []string{req.BaseRoot, req.LocalRoot, req.IncomingRoot} {
			write(t, root, name, data)
			info, err := os.Stat(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			before[filepath.Join(root, name)] = info
		}
	}
	report := run(t, req)
	if len(report.Items) != len(fixtures) {
		t.Fatalf("items %d", len(report.Items))
	}
	metadata := map[string]*Metadata{}
	for _, item := range report.Items {
		if item.Status != "same" {
			t.Fatal(item)
		}
		metadata[item.Path] = item.Local
	}
	for name, expected := range map[string]struct {
		encoding, bom, eol string
		newline            bool
	}{
		"обычный.txt": {"utf-8", "none", "lf", true}, "crlf.txt": {"utf-8", "none", "crlf", true}, "mixed.txt": {"utf-8", "none", "mixed", true},
		"empty.txt": {"utf-8", "none", "none", false}, "no-final.txt": {"utf-8", "none", "none", false}, "bom.txt": {"utf-8", "utf-8", "lf", true},
		"utf16.txt": {"utf-16le", "utf-16le", "crlf", true}, "binary.bin": {"unknown", "none", "none", false}, "legacy.txt": {"unknown", "none", "none", false},
	} {
		m := metadata[name]
		if m.Encoding != expected.encoding || m.BOM != expected.bom || m.EOL != expected.eol || m.FinalNewline != expected.newline {
			t.Errorf("%s: %+v", name, m)
		}
	}
	for path, info := range before {
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !sameObservation(info, after) {
			t.Errorf("file changed: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(fixtures[filepath.Base(path)]) {
			t.Errorf("bytes changed: %s", path)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "привет") {
		t.Fatal("source content leaked into metadata")
	}
}

func TestRepresentationDifferencesDoNotDisappear(t *testing.T) {
	req := roots(t)
	write(t, req.LocalRoot, "text.txt", []byte("one\ntwo\n"))
	write(t, req.IncomingRoot, "text.txt", []byte("one\r\ntwo\r\n"))
	req.BaseRoot = ""
	item := run(t, req).Items[0]
	if item.Status != "needs-review" || !item.RepresentationChanged || item.Local.SHA256 == item.Incoming.SHA256 {
		t.Fatalf("%+v", item)
	}
}

func TestMalformedUTF16RemainsOpaque(t *testing.T) {
	for _, data := range [][]byte{{0xff, 0xfe, 0, 0xd8}, {0xfe, 0xff, 0xdc, 0}, {0xff, 0xfe, 'a'}} {
		metadata := Metadata{}
		analyzeRepresentation(data, &metadata)
		if metadata.Encoding != "unknown" || metadata.EOL != "none" || metadata.FinalNewline {
			t.Fatalf("malformed UTF-16 described as text: %+v", metadata)
		}
	}
}

func TestExclusionsAreExplicit(t *testing.T) {
	req := roots(t)
	for _, root := range []string{req.LocalRoot, req.IncomingRoot} {
		write(t, root, "visible.txt", []byte("ok"))
		for _, name := range []string{".git/config", "node_modules/lib/a.js", ".artifacts/run.json", "dist/app.js", "coverage/a.json", ".env", ".env.local", "key.pem", "key.pfx", "key.agekey"} {
			write(t, root, name, []byte("private"))
		}
	}
	report := run(t, req)
	if len(report.Items) != 1 || report.Items[0].Path != "visible.txt" || !report.Complete {
		t.Fatalf("%+v", report)
	}
	if len(report.Warnings) != 20 {
		t.Fatalf("warnings: %d", len(report.Warnings))
	}
	for _, warning := range report.Warnings {
		if warning.Code != "PATH_EXCLUDED" || warning.Path == "" {
			t.Fatal(warning)
		}
	}
}

func TestLimitsDoNotReturnPartialEquality(t *testing.T) {
	cases := []struct {
		name, code string
		limits     Limits
		files      map[string]string
	}{
		{"file-size", "FILE_SIZE_LIMIT", Limits{MaxFiles: 10, MaxFileBytes: 2, MaxTotalBytes: 100}, map[string]string{"a": "123"}},
		{"total-size", "TOTAL_SIZE_LIMIT", Limits{MaxFiles: 10, MaxFileBytes: 10, MaxTotalBytes: 3}, map[string]string{"a": "12", "b": "34"}},
		{"file-count", "FILE_COUNT_LIMIT", Limits{MaxFiles: 1, MaxFileBytes: 10, MaxTotalBytes: 100}, map[string]string{"a": "1", "b": "2"}},
		{"invalid", "INVALID_LIMITS", Limits{}, map[string]string{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			req := roots(t)
			for name, value := range test.files {
				write(t, req.LocalRoot, name, []byte(value))
			}
			report, err := Run(context.Background(), req, test.limits)
			if ErrorCode(err) != test.code {
				t.Fatalf("got %v (%s), want %s", err, ErrorCode(err), test.code)
			}
			if report.Complete || len(report.Items) > 0 {
				t.Fatal("partial report escaped")
			}
		})
	}
}

func TestCancellationAndRootValidation(t *testing.T) {
	req := roots(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, req, DefaultLimits()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, path := range []string{"relative/path", `\\server\share`, `\\.\pipe\figmobil`, `\\?\C:\`} {
		req.LocalRoot = path
		if _, err := Run(context.Background(), req, DefaultLimits()); ErrorCode(err) != "INVALID_ROOT" {
			t.Fatalf("path %q: %v", path, err)
		}
	}
}

func TestLinksNeverBecomeAddedOrDeletedFiles(t *testing.T) {
	req := roots(t)
	outside := t.TempDir()
	write(t, outside, "secret.txt", []byte("never read"))
	link := filepath.Join(req.LocalRoot, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("OS does not permit creating symlink: %v", err)
	}
	write(t, req.IncomingRoot, "linked/secret.txt", []byte("different"))
	write(t, req.LocalRoot, "ok.txt", []byte("ok"))
	write(t, req.IncomingRoot, "ok.txt", []byte("ok"))
	report := run(t, req)
	if report.Complete || len(report.Items) != 1 || report.Items[0].Path != "ok.txt" {
		t.Fatalf("%+v", report)
	}
	if len(report.Warnings) != 1 || report.Warnings[0].Code != "UNSUPPORTED_FILE_TYPE" {
		t.Fatalf("%+v", report.Warnings)
	}
	req.LocalRoot = link
	if _, err := Run(context.Background(), req, DefaultLimits()); ErrorCode(err) != "INVALID_ROOT" {
		t.Fatalf("root symlink accepted: %v", err)
	}
}

func TestFileDirectoryAndCaseConflicts(t *testing.T) {
	t.Run("file-directory", func(t *testing.T) {
		req := roots(t)
		write(t, req.LocalRoot, "a/child.txt", []byte("local"))
		write(t, req.IncomingRoot, "a", []byte("incoming"))
		report := run(t, req)
		if len(report.Items) != 2 {
			t.Fatal(report.Items)
		}
		for _, item := range report.Items {
			if item.Status != "conflict" {
				t.Fatal(item)
			}
		}
		if report.Warnings[0].Code != "PATH_TYPE_CONFLICT" {
			t.Fatal(report.Warnings)
		}
	})
	t.Run("case-file", func(t *testing.T) {
		req := roots(t)
		write(t, req.LocalRoot, "a.txt", []byte("a"))
		write(t, req.IncomingRoot, "A.txt", []byte("a"))
		report := run(t, req)
		if len(report.Items) != 2 {
			t.Fatal(report.Items)
		}
		for _, item := range report.Items {
			if item.Status != "conflict" {
				t.Fatal(item)
			}
		}
		if report.Warnings[0].Code != "PATH_CASE_COLLISION" {
			t.Fatal(report.Warnings)
		}
	})
	t.Run("case-directory", func(t *testing.T) {
		req := roots(t)
		write(t, req.LocalRoot, "a/local.txt", []byte("a"))
		write(t, req.IncomingRoot, "A/incoming.txt", []byte("a"))
		report := run(t, req)
		for _, item := range report.Items {
			if item.Status != "conflict" {
				t.Fatal(item)
			}
		}
	})
}

func TestObservationsDetectChangesAfterReading(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", []byte("old"))
	budget := scanBudget{limits: DefaultLimits()}
	tree, err := scanTree(context.Background(), root, &budget)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.root.Close()
	write(t, root, "a.txt", []byte("different length"))
	if err := tree.verify(context.Background()); ErrorCode(err) != "SOURCE_CHANGED" {
		t.Fatalf("changed source accepted: %v", err)
	}
}

func TestEmptyAndSameRoots(t *testing.T) {
	req := roots(t)
	report := run(t, req)
	if !report.Complete || len(report.Items) != 0 || report.Items == nil || report.Warnings == nil {
		t.Fatalf("%+v", report)
	}
	write(t, req.LocalRoot, "a.txt", []byte("a"))
	req.IncomingRoot = req.LocalRoot
	req.BaseRoot = ""
	report = run(t, req)
	if report.Items[0].Status != "same" {
		t.Fatal(report.Items)
	}
}

func TestDepthLimit(t *testing.T) {
	req := roots(t)
	nested := strings.Repeat("a/", 130) + "file.txt"
	write(t, req.LocalRoot, nested, []byte("x"))
	_, err := Run(context.Background(), req, DefaultLimits())
	if ErrorCode(err) != "TREE_DEPTH_LIMIT" {
		t.Fatalf("deep tree accepted: %v", err)
	}
}
