package compare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ReadGitPolicy runs only Git's read-only config/check-attr builtins. It never
// runs filters, project code, hooks, status, checkout, or index-writing commands.
// Initial support deliberately requires raw byte checkout rules; other rules
// remain readable/capturable but cannot be applied by the current writer.
func ReadGitPolicy(ctx context.Context, root string, paths []string) GitPolicy {
	unsupported := func(reason string) GitPolicy { return GitPolicy{Kind: "unknown", Supported: false, Reason: reason} }
	git, err := exec.LookPath("git")
	if err != nil {
		return unsupported("Git недоступен: политика рабочей копии не проверена.")
	}
	for _, entry := range os.Environ() {
		upper := strings.ToUpper(strings.SplitN(entry, "=", 2)[0])
		if strings.HasPrefix(upper, "GIT_CONFIG_") || upper == "GIT_ATTR_SOURCE" || upper == "GIT_INDEX_FILE" || upper == "GIT_WORK_TREE" || upper == "GIT_DIR" || upper == "GIT_COMMON_DIR" || upper == "GIT_OBJECT_DIRECTORY" || upper == "GIT_ALTERNATE_OBJECT_DIRECTORIES" {
			return unsupported("Внешние переопределения Git config требуют отдельной проверки.")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	command := func(input []byte, args ...string) ([]byte, error) {
		prefix := []string{"--no-pager", "--no-optional-locks", "-C", root, "-c", "core.fsmonitor=false", "-c", "core.hooksPath=NUL"}
		cmd := exec.CommandContext(ctx, git, append(prefix, args...)...)
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
		cmd.Stdin = bytes.NewReader(input)
		output := &limitedOutput{limit: 2 << 20}
		cmd.Stdout = output
		cmd.Stderr = io.Discard
		err := cmd.Run()
		if output.exceeded {
			return nil, errors.New("git metadata output limit")
		}
		return output.data.Bytes(), err
	}
	_, err = command(nil, "config", "--local", "--get", "core.repositoryformatversion")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 128 && !hasGitMarker(root) {
			return GitPolicy{Kind: "raw-v1", Fingerprint: policyHash([]byte("raw-v1")), Supported: true}
		}
		return unsupported("Не удалось определить политику Git выбранной папки.")
	}
	config, err := command(nil, "config", "--null", "--get-regexp", `^(core\.(autocrlf|eol|safecrlf|filemode|symlinks|attributesfile)|filter\..*)$`)
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return unsupported("Не удалось прочитать конфигурацию Git.")
		}
	}
	values := map[string]string{}
	for _, entry := range bytes.Split(config, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		pair := strings.SplitN(string(entry), "\n", 2)
		if len(pair) != 2 {
			return unsupported("Неизвестный формат конфигурации Git.")
		}
		values[pair[0]] = pair[1]
	}
	for key, value := range values {
		if (key == "core.autocrlf" && value != "false") || (key == "core.eol" && value != "lf") {
			return unsupported("Фильтры или преобразования checkout пока не поддерживаются.")
		}
	}
	var input bytes.Buffer
	ordered := append([]string{}, paths...)
	sort.Strings(ordered)
	for _, name := range ordered {
		if err := ValidateRelativePath(name); err != nil {
			return unsupported("Недопустимый путь проверки Git attributes.")
		}
		input.WriteString(name)
		input.WriteByte(0)
	}
	attrs, err := command(input.Bytes(), "check-attr", "-z", "--stdin", "text", "eol", "working-tree-encoding", "filter", "ident")
	if err != nil {
		return unsupported("Не удалось прочитать эффективные Git attributes.")
	}
	parts := bytes.Split(attrs, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) != len(paths)*15 {
		return unsupported("Неполная проверка эффективных Git attributes.")
	}
	for i := 2; i < len(parts); i += 3 {
		if string(parts[i]) != "unspecified" && string(parts[i]) != "unset" {
			return unsupported("Git attributes изменяют представление файлов: применение заблокировано.")
		}
	}
	index, err := command(nil, "ls-files", "--stage", "-z")
	if err != nil {
		return unsupported("Не удалось проверить типы и режимы файлов Git index.")
	}
	if reason := unsupportedIndexMode(index, paths); reason != "" {
		return unsupported(reason)
	}
	return GitPolicy{Kind: "raw-v1", Fingerprint: policyHash([]byte("raw-v1")), Supported: true}
}

func unsupportedIndexMode(index []byte, paths []string) string {
	selected := map[string]bool{}
	ancestors := map[string]bool{}
	for _, name := range paths {
		key := strings.ToLower(name)
		selected[key] = true
		for parent := filepath.ToSlash(filepath.Dir(name)); parent != "." && parent != "/"; parent = filepath.ToSlash(filepath.Dir(parent)) {
			ancestors[strings.ToLower(parent)] = true
		}
	}
	seen := map[string]bool{}
	for _, entry := range bytes.Split(index, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		pair := bytes.SplitN(entry, []byte{'\t'}, 2)
		if len(pair) != 2 {
			return "Неизвестный формат Git index."
		}
		fields := strings.Fields(string(pair[0]))
		if len(fields) != 3 {
			return "Неизвестный формат режима Git index."
		}
		key := strings.ToLower(string(pair[1]))
		if fields[0] == "160000" && ancestors[key] {
			return "Вложенный submodule требует отдельного профиля или явного исключения."
		}
		if !selected[key] {
			continue
		}
		if fields[0] != "100644" || fields[2] != "0" || seen[key] {
			return "Executable/symlink/gitlink, unmerged index или коллизия регистра пока не поддерживаются."
		}
		seen[key] = true
	}
	return ""
}

type limitedOutput struct {
	data     bytes.Buffer
	limit    int
	exceeded bool
}

func hasGitMarker(root string) bool {
	for current := filepath.Clean(root); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil || !os.IsNotExist(err) {
			return true
		}
		if filepath.Dir(current) == current {
			return false
		}
	}
}

func (w *limitedOutput) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.data.Len() {
		w.exceeded = true
		return 0, errors.New("output limit")
	}
	return w.data.Write(data)
}
func policyHash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
