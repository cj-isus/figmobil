package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Git is used only in an isolated synthetic test, never by the application.
func fixtureGit(t *testing.T, root, config string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=" + filepath.Dir(config), "-c", "core.autocrlf=false"}, args...)...)
	command.Dir = root
	for _, value := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(strings.SplitN(value, "=", 2)[0]), "GIT_") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+config, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic git %v: %v %s", args, err, output)
	}
	return output
}

func TestRealComparisonPreservesBytesMtimeStagingAndGitDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable for isolated integration fixture")
	}
	base := t.TempDir()
	local := t.TempDir()
	incoming := t.TempDir()
	configuration := filepath.Join(t.TempDir(), "empty-git-config")
	if err := os.WriteFile(configuration, nil, 0600); err != nil {
		t.Fatal(err)
	}
	initial := []byte("alpha\r\nbeta\r\n")
	for _, root := range []string{base, local, incoming} {
		if err := os.WriteFile(filepath.Join(root, "sample.js"), initial, 0644); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, local, configuration, "init", "--template="+filepath.Dir(configuration))
	fixtureGit(t, local, configuration, "add", "sample.js")
	current := []byte("alpha\r\nbeta local\r\n")
	if err := os.WriteFile(filepath.Join(local, "sample.js"), current, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incoming, "sample.js"), []byte("alpha incoming\r\nbeta\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, ".env"), []byte("SYNTHETIC_FIXTURE_ONLY=1"), 0600); err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(filepath.Join(local, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	statBefore, err := os.Stat(filepath.Join(local, "sample.js"))
	if err != nil {
		t.Fatal(err)
	}
	diffBefore := fixtureGit(t, local, configuration, "diff", "--no-ext-diff", "--no-textconv", "--raw")
	h := testHandler(t)
	input, err := json.Marshal(map[string]string{"baseRoot": base, "localRoot": local, "incomingRoot": incoming})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodPost, "/api/compare", string(input)))
	if w.Code != 200 {
		t.Fatalf("comparison: %d %s", w.Code, w.Body.String())
	}
	var report struct {
		Complete bool `json:"complete"`
		Items    []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Complete || len(report.Items) != 1 || report.Items[0].Status != "conflict" {
		t.Fatalf("unexpected report: %s", w.Body.String())
	}
	fileAfter, err := os.ReadFile(filepath.Join(local, "sample.js"))
	if err != nil {
		t.Fatal(err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(local, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	statAfter, err := os.Stat(filepath.Join(local, "sample.js"))
	if err != nil {
		t.Fatal(err)
	}
	diffAfter := fixtureGit(t, local, configuration, "diff", "--no-ext-diff", "--no-textconv", "--raw")
	if !bytes.Equal(current, fileAfter) || !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Fatal("working file changed")
	}
	if sha256.Sum256(indexBefore) != sha256.Sum256(indexAfter) {
		t.Fatal("Git staging changed")
	}
	if !bytes.Equal(diffBefore, diffAfter) {
		t.Fatal("ordinary Git diff changed")
	}
}
