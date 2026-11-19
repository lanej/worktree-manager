//go:build integration

package acceptance

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unicode/utf8"
)

func policyRequire(t *testing.T, condition bool, description string, values ...any) {
	t.Helper()

	if !condition {
		t.Fatalf(description, values...)
	}
}

func policyJSON(t *testing.T, text string) map[string]any {
	t.Helper()

	var value map[string]any

	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()

	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode policy JSON: %v\n%s", err, text)
	}

	return value
}

func policyMap(t *testing.T, value any) map[string]any {
	t.Helper()

	object, ok := value.(map[string]any)
	policyRequire(t, ok, "expected object, got %#v", value)

	return object
}

func policyRows(t *testing.T, value any) []map[string]any {
	t.Helper()

	rows, ok := value.([]any)
	policyRequire(t, ok, "expected rows, got %#v", value)

	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, policyMap(t, row))
	}

	return result
}

func policyNumber(t *testing.T, value any) int64 {
	t.Helper()

	valueNumber, ok := value.(json.Number)
	policyRequire(t, ok, "expected a JSON number, got %#v", value)

	number, err := valueNumber.Int64()
	if err != nil {
		t.Fatalf("expected integer, got %#v: %v", value, err)
	}

	return number
}

func policyRun(f *fixture, directory string, success bool, extra map[string]string, args ...string) result {
	f.t.Helper()
	previous := f.env

	f.env = maps.Clone(previous)
	defer func() { f.env = previous }()

	for key, value := range extra {
		f.env[key] = value
	}

	out := f.execute(directory, append([]string{f.binary, "policy"}, args...)...)

	expected := 0
	if !success {
		expected = 1
	}

	policyRequire(f.t, out.code == expected, "policy %q status %d, want %d: %s%s", args, out.code, expected, out.stdout, out.stderr)

	return out
}

func policyRandomFile(t *testing.T, path string, size int) {
	t.Helper()

	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func policyAllocated(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	policyRequire(t, ok, "missing native allocated size for %s", path)

	return stat.Blocks * 512
}

func policyChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func policyRemove(t *testing.T, path string) {
	t.Helper()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func policyRename(t *testing.T, before, after string) {
	t.Helper()

	if err := os.Rename(before, after); err != nil {
		t.Fatal(err)
	}
}

func policySymlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func policyDirectoryFiles(t *testing.T, directory string) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}

	result := map[string]string{}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		//nolint:gosec // rationale: Snapshot only files enumerated in this test's isolated fixture directory.
		content, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}

		result[entry.Name()] = string(content)
	}

	return result
}

func policyEmptyDirectory(t *testing.T, directory string) bool {
	t.Helper()

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}

	return len(entries) == 0
}

func TestPolicyHooksBudgetsAndRegistry(t *testing.T) {
	f := newFixture(t)
	f.env["HOME"] = f.root
	f.env["XDG_STATE_HOME"] = filepath.Join(f.root, "state")
	f.env["GIT_TERMINAL_PROMPT"] = "0"
	f.git("init", "--initial-branch=main")
	f.git("config", "user.name", "Policy Test")
	f.git("config", "user.email", "policy@example.invalid")
	f.git("commit", "--allow-empty", "-m", "initial")

	run := func(success bool, args ...string) result { return policyRun(f, f.repo, success, nil, args...) }
	state := func() map[string]any { return policyJSON(t, run(true, "refresh").stdout) }
	hooks := filepath.Join(f.repo, ".git/hooks")
	previous := filepath.Join(hooks, "post-checkout")
	log := filepath.Join(f.root, "forwarded")
	f.write(previous, "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '"+log+"'\nexit 7\n")
	policyChmod(t, previous, 0o755)
	run(true, "install")

	installed := f.read(previous)

	run(true, "install")
	policyRequire(t, f.read(previous) == installed, "reinstallation changed existing hooks")
	policyRequire(t, policyNumber(t, state()["linked_count"]) == 0, "unexpected initial linked count")
	run(true, "preflight")
	f.git("config", "worktree.limit", "1")
	first := filepath.Join(f.root, "linked with spaces")
	added := f.execute(f.repo, f.gitBinary, "worktree", "add", "-b", "first", first)
	policyRequire(t, added.code == 7 && strings.HasSuffix(f.read(log), "\n1\n"), "original hook was not forwarded: %+v", added)
	snapshot := policyJSON(t, f.read(filepath.Join(f.repo, ".git/worktree-policy/inventory.json")))
	policyRequire(t, policyNumber(t, snapshot["linked_count"]) == 1 && policyRows(t, snapshot["worktrees"])[1]["worktree"] == first && strings.Contains(added.stderr, "limit is 1"), "hook did not record surviving checkout: %+v", snapshot)
	run(false, "preflight")
	policyRun(f, first, false, nil, "preflight")
	blocked := filepath.Join(f.root, "blocked")
	native := run(false, "hook", "pre-worktree", "add", "", "", blocked)
	policyRequire(t, native.code == 1 && strings.Contains(native.stderr, "refusing add") && !f.exists(blocked), "native veto failed: %+v", native)
	run(true, "hook", "pre-worktree", "remove", "first", first, "")

	checkout := f.execute(f.repo, f.gitBinary, "checkout", "main")
	policyRequire(t, checkout.code != 0 && policyNumber(t, state()["linked_count"]) == 1, "ordinary checkout lost forwarded failure")

	original := filepath.Join(hooks, "post-checkout.worktree-policy-original")
	policyChmod(t, original, 0o644)
	f.git("checkout", "main")
	policyChmod(t, original, 0o755)

	second := filepath.Join(f.root, "no checkout\nwith newline")
	f.git("worktree", "add", "--no-checkout", "-b", "second", second)
	policyRequire(t, policyNumber(t, state()["linked_count"]) == 2, "no-checkout registration missing")
	policyFindTree(t, state(), second)

	moved := filepath.Join(f.root, "moved")
	f.git("worktree", "move", second, moved)
	run(true, "hook", "post-worktree", "move", "second", second, moved)
	policyFindTree(t, state(), moved)
	f.git("worktree", "remove", "--force", moved)
	run(true, "hook", "post-worktree", "remove", "second", moved, "")
	policyRequire(t, policyNumber(t, state()["linked_count"]) == 1, "removed checkout retained in inventory")

	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}

	f.git("worktree", "prune", "--expire", "now")
	run(true, "hook", "post-worktree", "prune", "first", first, "")
	policyRequire(t, policyNumber(t, state()["linked_count"]) == 0, "pruned checkout retained in inventory")
	f.git("config", "worktree.limit", "0")
	run(false, "preflight")
	f.git("config", "--unset", "worktree.limit")
	run(true, "hook", "pre-worktree", "add", "", "", first)
	f.git("config", "worktree.limit", "invalid")
	run(false, "preflight")

	checkout = f.execute(f.repo, f.gitBinary, "checkout", "main")
	policyRequire(t, checkout.code != 0 && strings.Contains(checkout.stderr, "nonnegative integer"), "invalid policy was not reported without suppressing original hook failure: %+v", checkout)
	f.git("config", "--unset", "worktree.limit")
	budgetTree := filepath.Join(f.root, "budget tree")
	f.git("worktree", "add", "--no-checkout", "-b", "budget", budgetTree)
	payload, outside := filepath.Join(budgetTree, "ignored build output"), filepath.Join(f.root, "outside payload")
	policyRandomFile(t, payload, 32768)
	policyRandomFile(t, outside, 1024*1024)
	policySymlink(t, outside, filepath.Join(budgetTree, "external symlink"))

	if err := os.Link(payload, filepath.Join(budgetTree, "hardlink")); err != nil {
		t.Fatal(err)
	}

	f.git("config", "worktree.maxBytes", "1B")
	policyRequire(t, strings.Contains(run(false, "preflight").stderr, "repository allocated bytes"), "local byte budget did not veto")

	movedBudget := filepath.Join(f.root, "temporarily moved tree")
	policyRename(t, budgetTree, movedBudget)

	denied := run(false, "preflight")
	policyRequire(t, denied.code == 1 && strings.Contains(strings.ToLower(denied.stderr), "no such file"), "missing checkout did not fail closed: %+v", denied)
	policyChmod(t, original, 0o644)

	checkout = f.execute(f.repo, f.gitBinary, "checkout", "main")
	policyRequire(t, checkout.code == 0 && strings.Contains(strings.ToLower(checkout.stderr), "no such file"), "observation blocked ordinary checkout: %+v", checkout)
	policyChmod(t, original, 0o755)
	policyRename(t, movedBudget, budgetTree)

	other := filepath.Join(f.root, "other repo")
	f.git("init", "--initial-branch=main", other)
	policyRun(f, other, true, nil, "preflight")
	f.git("config", "--unset", "worktree.maxBytes")
	f.git("config", "worktree.limit", "1")
	run(false, "preflight")
	policyRun(f, other, true, nil, "preflight")
	f.git("config", "--unset", "worktree.limit")
	run(true, "configure", "--global", "--max-bytes", "1KiB")

	denied = policyRun(f, other, false, nil, "preflight")
	policyRequire(t, denied.code == 1 && strings.Contains(denied.stderr, "global allocated bytes"), "global byte budget did not veto")
	run(true, "configure", "--global", "--max-bytes", "off", "--max-worktrees", "1")
	policyRun(f, other, false, nil, "preflight")
	report := policyJSON(t, policyRun(f, f.root, true, nil, "status", "--all").stdout)
	policyRequire(t, report["complete"] == true && len(policyRows(t, report["repositories"])) == 2 && policyNumber(t, policyMap(t, report["totals"])["linked_count"]) == 1, "bad global inventory: %+v", report)
	allocated := policyNumber(t, policyMap(t, report["totals"])["disk_bytes"])
	policyRequire(t, allocated >= policyAllocated(t, payload) && allocated < policyAllocated(t, outside), "symlink/hardlink accounting changed: %d", allocated)
	run(true, "configure", "--global", "--max-worktrees", "off")
	policyRun(f, other, true, nil, "preflight")
	missing := filepath.Join(f.root, "temporarily unavailable")
	policyRename(t, other, missing)
	report = policyJSON(t, run(false, "status", "--all").stdout)
	policyRequire(t, report["complete"] == false, "missing repository reported complete")
	absent := policyFindRepository(t, report, filepath.Join(other, ".git"))
	policyRequire(t, absent["available"] == false, "missing repository reported available")

	unavailable := 0

	for _, row := range policyRows(t, report["repositories"]) {
		if row["available"] == false {
			unavailable++
		}
	}

	policyRequire(t, unavailable == 1, "unexpected unavailable repositories: %+v", report)
	run(true, "configure", "--global", "--max-worktrees", "10")
	policyRequire(t, strings.Contains(run(false, "preflight").stderr, "cannot evaluate global budget"), "unknown global inventory did not fail closed")
	policyRequire(t, f.exists(payload) && f.exists(outside), "budget checks removed files")
	policyRun(f, missing, true, nil, "refresh")
	policySymlink(t, missing, other)
	redirected := policyJSON(t, run(false, "status", "--all").stdout)
	historical := policyFindRepository(t, redirected, filepath.Join(other, ".git"))
	policyRequire(t, historical["available"] == false && strings.Contains(fmt.Sprint(historical["error"]), "resolves elsewhere") && redirected["complete"] == false, "redirected historical registration was adopted")
	policyRequire(t, policyFindRepository(t, redirected, filepath.Join(missing, ".git"))["available"] == true, "new registration unavailable")
	retired := policyJSON(t, run(true, "forget", filepath.Join(other, ".git")).stdout)
	policyRequire(t, retired["forgotten_common_gitdir"] == filepath.Join(other, ".git") && !f.exists(fmt.Sprint(retired["removed_registry_record"])), "forget did not retire exact registry record")
	run(true, "preflight")
	reconciled := policyJSON(t, run(true, "status", "--all").stdout)
	policyRequire(t, reconciled["complete"] == true && policyNumber(t, policyMap(t, reconciled["totals"])["linked_count"]) == 1 && len(policyRows(t, reconciled["repositories"])) == 2, "registry did not reconcile")
	policyFindRepository(t, reconciled, filepath.Join(f.repo, ".git"))
	policyFindRepository(t, reconciled, filepath.Join(missing, ".git"))
	policyRequire(t, f.exists(filepath.Join(missing, ".git/config")) && f.exists(payload), "forget removed repository data")
	run(false, "forget", filepath.Join(other, ".git"))
	run(true, "configure", "--global", "--max-worktrees", "off")
	policyRemove(t, other)
	policyRename(t, missing, other)
	run(true, "forget", filepath.Join(missing, ".git"))
	policyRun(f, other, true, nil, "refresh")
	activeHooks, activeConfig := f.read(previous), f.read(filepath.Join(f.repo, ".git/config"))
	run(true, "forget", filepath.Join(f.repo, ".git"))
	policyRequire(t, f.read(previous) == activeHooks && f.read(filepath.Join(f.repo, ".git/config")) == activeConfig && f.exists(payload) && f.exists(filepath.Join(f.repo, ".git/worktree-policy/inventory.json")), "forget changed hooks, configuration, or checkout data")
	policyRequire(t, len(policyRows(t, policyJSON(t, run(true, "status", "--all").stdout)["repositories"])) == 1, "forget retained registration")
	run(true, "hook", "post-worktree", "add", "budget", "", budgetTree)
	policyRequire(t, len(policyRows(t, policyJSON(t, run(true, "status", "--all").stdout)["repositories"])) == 2, "native observation did not restore registration")
	policyCheckHookConfiguration(t, f, other)
	policyCheckEnvironmentAndLegacy(t, f)
	policyCheckGlobalActivation(t, f)
}

func policyFindTree(t *testing.T, state map[string]any, path string) {
	t.Helper()

	for _, row := range policyRows(t, state["worktrees"]) {
		if row["worktree"] == path {
			return
		}
	}

	t.Fatalf("missing worktree %q in %+v", path, state)
}

func policyFindRepository(t *testing.T, report map[string]any, directory string) map[string]any {
	t.Helper()

	for _, row := range policyRows(t, report["repositories"]) {
		if row["common_gitdir"] == directory {
			return row
		}
	}

	t.Fatalf("missing repository %q in %+v", directory, report)

	return nil
}

func policyCheckHookConfiguration(t *testing.T, f *fixture, other string) {
	t.Helper()

	custom := filepath.Join(f.root, "custom hooks")
	f.mkdir(custom)
	f.git("config", "core.hooksPath", "../custom hooks")
	rejected := policyRun(f, f.repo, false, nil, "install")
	policyRequire(t, rejected.code == 1 && strings.Contains(rejected.stderr, "core.hooksPath") && policyEmptyDirectory(t, custom) && f.git("config", "--get", "core.hooksPath") == "../custom hooks", "custom hooks were overwritten")
	f.git("config", "--unset", "core.hooksPath")
	f.git("config", "extensions.worktreeConfig", "true")
	f.git("config", "--worktree", "core.hooksPath", custom)
	policyRun(f, f.repo, false, nil, "install")
	policyRequire(t, policyEmptyDirectory(t, custom), "worktree-local hooks were overwritten")
	f.gitAt(other, "config", "user.name", "Policy Test")
	f.gitAt(other, "config", "user.email", "policy@example.invalid")
	f.gitAt(other, "commit", "--allow-empty", "-m", "initial")
	f.gitAt(other, "config", "extensions.worktreeConfig", "true")
	f.gitAt(other, "config", "--worktree", "policy.keep", "main setting")
	linked := filepath.Join(f.root, "inactive linked worktree")
	f.gitAt(other, "worktree", "add", "-b", "inactive", linked)
	f.gitAt(linked, "config", "--worktree", "policy.keep", "linked setting")
	linkedConfig := f.gitAt(linked, "rev-parse", "--path-format=absolute", "--git-path", "config.worktree")
	configFiles := []string{filepath.Join(other, ".git/config"), filepath.Join(other, ".git/config.worktree"), linkedConfig}
	readConfigs := func() []string {
		result := make([]string, 0, len(configFiles))
		for _, path := range configFiles {
			result = append(result, f.read(path))
		}

		return result
	}
	before := readConfigs()

	policyRun(f, other, true, nil, "install")
	policyRequire(t, reflect.DeepEqual(readConfigs(), before), "installation changed inactive worktree settings")
	f.gitAt(other, "checkout", "main")
	f.gitAt(linked, "config", "--worktree", "core.hooksPath", "../custom hooks")
	policyRemove(t, filepath.Join(other, ".git/hooks/post-worktree"))

	before = readConfigs()
	installedHooks := policyDirectoryFiles(t, filepath.Join(other, ".git/hooks"))
	rejected = policyRun(f, other, false, nil, "install")
	policyRequire(t, rejected.code == 1 && strings.Contains(rejected.stderr, "core.hooksPath") && reflect.DeepEqual(readConfigs(), before) && reflect.DeepEqual(policyDirectoryFiles(t, filepath.Join(other, ".git/hooks")), installedHooks), "rejected installation partially changed configuration or hooks")
}

func policyCheckEnvironmentAndLegacy(t *testing.T, f *fixture) {
	t.Helper()

	envRepo, envOther := filepath.Join(f.root, "environment invoking repo"), filepath.Join(f.root, "environment other repo")
	for _, directory := range []string{envRepo, envOther} {
		f.mkdir(directory)
		f.gitAt(directory, "init", "--initial-branch=main")
		f.gitAt(directory, "config", "user.name", "Policy Test")
		f.gitAt(directory, "config", "user.email", "policy@example.invalid")
		f.gitAt(directory, "commit", "--allow-empty", "-m", "initial")
	}

	envA, envB, envC := filepath.Join(f.root, "environment invoking linked"), filepath.Join(f.root, "environment other linked one"), filepath.Join(f.root, "environment other linked two")
	f.gitAt(envRepo, "worktree", "add", "--no-checkout", "-b", "a", envA)
	f.gitAt(envOther, "worktree", "add", "--no-checkout", "-b", "b", envB)
	f.gitAt(envOther, "worktree", "add", "--no-checkout", "-b", "c", envC)
	policyRandomFile(t, filepath.Join(envA, "build"), 32768)
	policyRandomFile(t, filepath.Join(envB, "build"), 131072)
	policyRandomFile(t, filepath.Join(envC, "build"), 65536)
	policyRun(f, envRepo, true, nil, "install")
	policyRun(f, envOther, true, nil, "refresh")
	clean := policyJSON(t, policyRun(f, envRepo, true, nil, "status", "--all").stdout)
	hookEnv := map[string]string{"GIT_COMMON_DIR": filepath.Join(envRepo, ".git"), "GIT_DIR": filepath.Join(envRepo, ".git"), "GIT_WORK_TREE": envRepo, "GIT_INDEX_FILE": filepath.Join(envRepo, ".git/nondefault-index")}
	contaminated := policyJSON(t, policyRun(f, envRepo, true, hookEnv, "status", "--all").stdout)
	policyRequire(t, reflect.DeepEqual(contaminated["totals"], clean["totals"]), "hook environment contaminated global totals")
	invoking := policyFindRepository(t, contaminated, filepath.Join(envRepo, ".git"))
	other := policyFindRepository(t, contaminated, filepath.Join(envOther, ".git"))
	policyRequire(t, policyNumber(t, policyMap(t, invoking["inventory"])["linked_count"]) == 1 && policyNumber(t, policyMap(t, other["inventory"])["linked_count"]) == 2 && policyNumber(t, other["disk_bytes"]) > policyNumber(t, invoking["disk_bytes"]), "hook environment contaminated per-repository inventory")
	cached := policyJSON(t, f.read(filepath.Join(envOther, ".git/worktree-policy/inventory.json")))
	policyRequire(t, policyNumber(t, cached["linked_count"]) == 2 && len(policyRows(t, cached["worktrees"])) == 3, "cross-repository cache changed")
	policyFindTree(t, cached, envB)
	policyFindTree(t, cached, envC)
	policyRun(f, f.repo, true, nil, "configure", "--global", "--max-worktrees", fmt.Sprint(policyMap(t, clean["totals"])["linked_count"]))

	for _, args := range [][]string{{"preflight"}, {"hook", "pre-worktree", "add", "", "", filepath.Join(f.root, "environment blocked")}} {
		denied := policyRun(f, envRepo, false, hookEnv, args...)
		policyRequire(t, denied.code == 1 && strings.Contains(denied.stderr, "global linked worktrees"), "contaminated hook bypassed count admission: %+v", denied)
	}

	policyRun(f, f.repo, true, nil, "configure", "--global", "--max-worktrees", "off", "--max-bytes", fmt.Sprint(policyMap(t, clean["totals"])["disk_bytes"]))
	denied := policyRun(f, envRepo, false, hookEnv, "preflight")
	policyRequire(t, denied.code == 1 && strings.Contains(denied.stderr, "global allocated bytes"), "contaminated hook bypassed byte admission")
	f.gitAt(envRepo, "config", "extensions.worktreeConfig", "true")
	globalPolicy, includedPolicy := filepath.Join(f.root, "deliberate global git policy"), filepath.Join(f.root, "conditional hook policy")
	f.git("config", "--file", includedPolicy, "core.hooksPath", filepath.Join(f.root, "custom hooks"))
	linkedGitdir := f.gitAt(envA, "rev-parse", "--absolute-git-dir")
	f.git("config", "--file", globalPolicy, "includeIf.gitdir:"+linkedGitdir+".path", includedPolicy)

	conditional := maps.Clone(hookEnv)
	conditional["GIT_CONFIG_GLOBAL"] = globalPolicy
	rejected := policyRun(f, envRepo, false, conditional, "install")
	policyRequire(t, rejected.code == 1 && strings.Contains(rejected.stderr, "core.hooksPath") && strings.Contains(rejected.stderr, linkedGitdir), "conditional linked-checkout hook policy was ignored")

	legacyName := "legacy-\xff"
	if runtime.GOOS == "darwin" {
		legacyName = "legacy-☃"
	}

	legacyRepo := filepath.Join(f.root, legacyName)
	f.git("init", "-q", "--initial-branch=main", legacyRepo)
	legacyCommon := filepath.Join(legacyRepo, ".git")
	legacyRoot := filepath.Join(f.root, "legacy-state")
	legacyRegistry := filepath.Join(legacyRoot, "git-worktree-policy/repositories")
	f.mkdir(legacyRegistry)

	digest := sha256.Sum256([]byte(legacyCommon))
	legacyRecord := filepath.Join(legacyRegistry, hex.EncodeToString(digest[:])+".json")
	encodedPath := strconv.QuoteToASCII(legacyCommon)
	encodedPath = regexp.MustCompile(`\\x([0-9a-f]{2})`).ReplaceAllString(encodedPath, `\udc$1`)
	f.write(legacyRecord, `{"common_gitdir":`+encodedPath+`,"inventory":{"linked_count":9876,"worktrees":[]}}`)
	migratedResult := policyRun(f, f.root, true, map[string]string{"XDG_STATE_HOME": legacyRoot}, "status", "--all")
	migrated := policyJSON(t, migratedResult.stdout)
	policyRequire(t, migrated["complete"] == true && len(policyRows(t, migrated["repositories"])) == 1 && policyNumber(t, policyMap(t, migrated["totals"])["linked_count"]) == 0 && len(policyDirectoryFiles(t, legacyRegistry)) == 1, "legacy registration did not migrate conservatively")

	if utf8.ValidString(legacyCommon) {
		policyFindRepository(t, migrated, legacyCommon)
	} else {
		policyRequire(t, strings.Contains(migratedResult.stdout, `legacy-\udcff`), "raw-byte legacy identity was lost: %s", migratedResult.stdout)
	}

	literalState := filepath.Join(f.root, "state[1]")
	isolated := map[string]string{"XDG_STATE_HOME": literalState}
	policyRun(f, envRepo, true, isolated, "refresh")
	policyRun(f, f.repo, true, isolated, "configure", "--global", "--max-worktrees", "1")
	literalReport := policyJSON(t, policyRun(f, f.repo, true, isolated, "status", "--all").stdout)
	policyRequire(t, literalReport["complete"] == true && policyNumber(t, policyMap(t, literalReport["totals"])["linked_count"]) == 1, "literal bracket state path changed registry selection")
	policyRun(f, envRepo, false, isolated, "preflight")

	registry := filepath.Join(literalState, "git-worktree-policy/repositories")
	if os.Geteuid() != 0 {
		policyChmod(t, registry, 0o300)
		func() {
			defer policyChmod(t, registry, 0o750)

			for _, args := range [][]string{{"status", "--all"}, {"preflight"}} {
				unreadable := policyRun(f, envRepo, false, isolated, args...)
				policyRequire(t, strings.Contains(strings.ToLower(unreadable.stderr), "permission denied"), "unreadable registry did not fail closed: %+v", unreadable)
			}

			notification := policyRun(f, envRepo, true, isolated, "observe", "post-checkout", "old", "new", "1")
			policyRequire(t, strings.Contains(strings.ToLower(notification.stderr), "permission denied"), "observation did not report unavailable registry")
		}()
	}
}

func policyCheckGlobalActivation(t *testing.T, f *fixture) {
	t.Helper()

	activation := filepath.Join(f.root, ".config/dotfiles/worktree-policy.gitconfig")
	f.mkdir(filepath.Dir(activation))
	f.write(activation, "[hook \"worktree-policy-pre\"]\n enabled = true\n[hook \"worktree-policy-post\"]\n enabled = true\n[hook \"worktree-policy-checkout\"]\n enabled = true\n")
	f.env["GIT_CONFIG_GLOBAL"] = filepath.Join(projectRoot(t), "testdata/git/gitconfig")
	f.env["XDG_STATE_HOME"] = filepath.Join(f.root, "global-state")
	bin := filepath.Join(f.root, ".local/bin")
	f.mkdir(bin)
	installedPolicy := filepath.Join(bin, "git-worktree-policy")
	policyPath := filepath.Join(projectRoot(t), "scripts/policy-compat")
	hookPath := filepath.Join(projectRoot(t), "testdata/bin/git-worktree-policy-hook")
	policySymlink(t, policyPath, installedPolicy)
	policySymlink(t, hookPath, filepath.Join(bin, "git-worktree-policy-hook"))
	nativePath := filepath.Join(f.root, "native-only-path")
	f.mkdir(nativePath)
	policySymlink(t, f.gitBinary, filepath.Join(nativePath, "git"))
	onlyGit := maps.Clone(f.env)
	onlyGit["PATH"], onlyGit["XDG_STATE_HOME"] = nativePath, filepath.Join(f.root, "native-only-state")
	previousEnv := f.env

	f.env = onlyGit
	for _, args := range [][]string{{"post-checkout", "old", "new", "1"}, {"pre-worktree", "add", "", "", "new"}} {
		out := f.execute(f.repo, append([]string{hookPath}, args...)...)
		policyRequire(t, out.code == 0, "native-only hook failed: %+v", out)
	}

	policyRemove(t, installedPolicy)
	policySymlink(t, filepath.Join(projectRoot(t), "testdata/bin/git-worktree-policy"), installedPolicy)

	warning := f.execute(f.repo, hookPath, "post-checkout", "old", "new", "1")
	policyRequire(t, warning.code == 0 && strings.Contains(warning.stderr, "native policy is unavailable"), "missing native policy observation did not warn")

	blocked := f.execute(f.repo, hookPath, "pre-worktree", "add", "", "", "new")
	policyRequire(t, blocked.code == 1 && strings.Contains(blocked.stderr, "native policy is unavailable"), "missing native policy did not veto admission")
	policyRemove(t, installedPolicy)
	policySymlink(t, policyPath, installedPolicy)

	f.env = previousEnv
	observed := filepath.Join(f.root, "globally observed repo")
	f.git("init", "--initial-branch=main", observed)

	supported := f.execute(observed, f.gitBinary, "hook", "list", "post-checkout")
	if supported.code != 0 {
		t.Log("Stock Git lacks configured hooks; the patched-Git hooks test covers global observation")
		return
	}

	f.gitAt(observed, "config", "user.name", "Global Policy Test")
	f.gitAt(observed, "config", "user.email", "global-policy@example.invalid")
	f.gitAt(observed, "commit", "--allow-empty", "-m", "initial")
	custom := filepath.Join(f.root, "global custom hooks")
	f.mkdir(custom)
	customLog := filepath.Join(f.root, "global custom log")
	customHook := filepath.Join(custom, "post-checkout")
	f.write(customHook, "#!/bin/sh\nprintf 'checkout\\n' >> '"+customLog+"'\n")
	policyChmod(t, customHook, 0o755)
	f.gitAt(observed, "config", "core.hooksPath", custom)
	globalLinked := filepath.Join(f.root, "globally observed linked")
	f.gitAt(observed, "worktree", "add", "-b", "global-observed", globalLinked)
	stored := filepath.Join(observed, ".git/worktree-policy/inventory.json")
	policyRequire(t, policyNumber(t, policyJSON(t, f.read(stored))["linked_count"]) == 1 && f.read(customLog) == "checkout\n", "configured global observation replaced custom checkout hook")
	report := policyJSON(t, policyRun(f, f.root, true, nil, "status", "--all").stdout)
	policyRequire(t, len(policyRows(t, report["repositories"])) == 1 && policyNumber(t, policyMap(t, report["totals"])["linked_count"]) == 1, "global observation did not register repository")
	f.gitAt(observed, "config", "hook.worktree-policy-checkout.enabled", "false")
	f.gitAt(observed, "worktree", "add", "-b", "global-disabled", filepath.Join(f.root, "tracking disabled linked"))
	policyRequire(t, policyNumber(t, policyJSON(t, f.read(stored))["linked_count"]) == 1 && f.read(customLog) == "checkout\ncheckout\n", "disabled global observation interfered with custom hook")
	refreshed := policyJSON(t, policyRun(f, observed, true, nil, "refresh").stdout)
	policyRequire(t, policyNumber(t, refreshed["linked_count"]) == 2, "explicit refresh did not discover disabled observation")
}
