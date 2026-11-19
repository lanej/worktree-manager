//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func lifecycleCheck(t *testing.T, condition bool, message string, args ...any) {
	t.Helper()

	if !condition {
		t.Fatalf(message, args...)
	}
}

func lifecycleJSON(t *testing.T, data string) map[string]any {
	t.Helper()

	var value map[string]any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		t.Fatalf("invalid JSON %q: %v", data, err)
	}

	return value
}

func lifecycleObject(t *testing.T, value any) map[string]any {
	t.Helper()

	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T: %v", value, value)
	}

	return object
}

func lifecycleRows(t *testing.T, value any) []any {
	t.Helper()

	rows, ok := value.([]any)
	if !ok {
		t.Fatalf("expected rows, got %T: %v", value, value)
	}

	return rows
}

func lifecycleRow(t *testing.T, report map[string]any, collection, key string, value any) map[string]any {
	t.Helper()

	for _, item := range lifecycleRows(t, report[collection]) {
		row := lifecycleObject(t, item)
		if row[key] == value {
			return row
		}
	}

	t.Fatalf("missing %s row %s=%v in %v", collection, key, value, report)

	return nil
}

func lifecycleKeys(t *testing.T, object map[string]any, keys ...string) {
	t.Helper()
	lifecycleCheck(t, len(object) == len(keys), "unexpected keys: %v; want %v", object, keys)

	for _, key := range keys {
		_, exists := object[key]
		lifecycleCheck(t, exists, "missing %s in %v", key, object)
	}
}

func lifecycleString(t *testing.T, object map[string]any, key string) string {
	t.Helper()

	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("%s is not a string: %v", key, object)
	}

	return value
}

func lifecycleInit(f *fixture) {
	f.git("init", "-q", "--initial-branch=main")
	f.write(filepath.Join(f.repo, ".gitignore"), ".worktrees/\nnode_modules/\n")
	f.write(filepath.Join(f.repo, "tracked"), "preserve branches\n")
	f.git("add", ".")
	f.git("commit", "-qm", "initial")
}

func lifecycleExecutable(f *fixture, path, content string) {
	f.t.Helper()
	f.write(path, content)

	//nolint:gosec // rationale: This executable shell fixture is owned by the test under t.TempDir.
	if err := os.Chmod(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func lifecycleRemove(f *fixture, path string) {
	f.t.Helper()

	if err := os.Remove(path); err != nil {
		f.t.Fatal(err)
	}
}

func lifecycleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestLifecycleAllocationRejections(t *testing.T) {
	f := newFixture(t)
	lifecycleInit(f)

	before := f.git("for-each-ref", "--format=%(refname)")
	for _, slug := range []string{"../escape", "Codex", "", strings.Repeat("a", 49)} {
		failure := f.fail(f.binary, "create", "--agent", slug, "--json")
		errorObject := lifecycleObject(t, lifecycleJSON(t, failure.stdout)["error"])
		lifecycleCheck(t, errorObject["code"] == "invalid_arguments", "unexpected error: %v", errorObject)
	}

	f.write(filepath.Join(f.repo, ".gitignore"), "node_modules/\n")
	lifecycleCheck(t, strings.Contains(f.fail(f.binary, "create", "--agent", "codex").stderr, "must be ignored"), "unignored destination was not rejected")
	f.write(filepath.Join(f.repo, ".gitignore"), ".worktrees/\nnode_modules/\n")
	outside := filepath.Join(f.root, "outside")
	f.mkdir(outside)

	if err := os.Symlink(outside, filepath.Join(f.repo, ".worktrees")); err != nil {
		t.Fatal(err)
	}

	lifecycleCheck(t, strings.Contains(f.fail(f.binary, "create", "--agent", "codex").stderr, "symlink"), "symlink destination was not rejected")

	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}

	lifecycleCheck(t, len(entries) == 0, "symlink rejection changed destination: %v", entries)
	lifecycleRemove(f, filepath.Join(f.repo, ".worktrees"))
	lifecycleCheck(t, f.git("for-each-ref", "--format=%(refname)") == before, "rejected allocations changed refs")
	records := filepath.Join(f.env["XDG_STATE_HOME"], "worktree-manager", "tasks")
	lifecycleCheck(t, !f.exists(records), "rejected allocations left task records")
	f.git("branch", "worktree")
	lifecycleCheck(t, strings.Contains(f.fail(f.binary, "create", "--agent", "codex").stderr, "namespace conflicts"), "branch namespace collision was not rejected")
	lifecycleCheck(t, !f.exists(records), "namespace rejection left task records")
	f.git("branch", "-d", "worktree")
	manual := f.call("create")
	lifecycleCheck(t, manual["agent"] == "manual" && manual["created_at"] != nil && manual["gitdir"] != nil, "manual response was compacted: %v", manual)
	f.call("finish", lifecycleString(t, manual, "id"))
	f.git("worktree", "remove", lifecycleString(t, manual, "path"))
}

func TestCallerResponses(t *testing.T) {
	for _, test := range []struct {
		name, agent, owner string
		env                map[string]string
	}{
		{"codex thread", "codex", "calling-agent-id", map[string]string{"CODEX_THREAD_ID": "calling-agent-id"}},
		{"codex session", "codex", "calling-agent-id", map[string]string{"CODEX_SESSION_ID": "calling-agent-id"}},
		{"claude session", "claude", "calling-agent-id", map[string]string{"CLAUDE_CODE_SESSION_ID": "calling-agent-id", "CLAUDECODE": "1"}},
		{"claude marker", "claude", "", map[string]string{"CLAUDECODE": "1"}},
		{"pi session", "pi", "calling-agent-id", map[string]string{"PI_SESSION_ID": "calling-agent-id", "PI_CODING_AGENT": "true", "AI_AGENT": "pi"}},
		{"pi marker", "pi", "", map[string]string{"PI_CODING_AGENT": "true"}},
		{"gemini marker", "gemini", "", map[string]string{"GEMINI_CLI": "1"}},
		{"opencode session", "opencode", "calling-agent-id", map[string]string{"OPENCODE_SESSION_ID": "calling-agent-id", "OPENCODE": "1", "AI_AGENT": "opencode"}},
		{"opencode marker", "opencode", "", map[string]string{"OPENCODE": "1"}},
		{"custom identity", "custom", "calling-agent-id", map[string]string{"WORKTREE_AGENT": "custom", "WORKTREE_OWNER_ID": "calling-agent-id"}},
		{"generic marker", "custom", "", map[string]string{"AI_AGENT": "custom"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			lifecycleInit(f)

			for name, value := range test.env {
				f.env[name] = value
			}

			for _, outputFlags := range [][]string{nil, {"--json"}, {"--verbose"}} {
				args := append([]string{f.binary, "create"}, outputFlags...)
				response := lifecycleJSON(t, f.run(args...).stdout)
				lifecycleKeys(t, response, "command", "result", "schema_version")
				lifecycleCheck(t, response["command"] == "create" && response["schema_version"] == float64(1), "unexpected envelope: %v", response)
				agentTask := lifecycleObject(t, response["result"])
				lifecycleCheck(t, agentTask["agent"] == test.agent, "agent mismatch: %v", agentTask)

				owner := ""
				if agentTask["owner_id"] != nil {
					owner = lifecycleString(t, agentTask, "owner_id")
				}

				lifecycleCheck(t, owner == test.owner, "owner mismatch: %v", agentTask)

				compactKeys := []string{"id", "path", "branch", "state", "agent"}
				if test.owner != "" {
					compactKeys = append(compactKeys, "owner_id")
				}

				if len(outputFlags) != 0 && outputFlags[0] == "--verbose" {
					for _, key := range []string{"agent", "common_gitdir", "gitdir", "created_at"} {
						lifecycleCheck(t, agentTask[key] != nil, "verbose response missing %s: %v", key, agentTask)
					}
				} else {
					lifecycleKeys(t, agentTask, compactKeys...)
				}

				path, id := lifecycleString(t, agentTask, "path"), lifecycleString(t, agentTask, "id")
				lifecycleCheck(t, f.exists(path) && agentTask["state"] == "active", "creation did not confirm checkout: %v", agentTask)

				for range 2 {
					finished := lifecycleObject(t, lifecycleJSON(t, f.run(f.binary, "finish", id).stdout)["result"])
					lifecycleKeys(t, finished, compactKeys...)
					lifecycleCheck(t, finished["id"] == id && finished["state"] == "finished", "finish was not idempotent: %v", finished)
				}

				verbose := lifecycleObject(t, lifecycleJSON(t, f.run(f.binary, "finish", id, "--verbose").stdout)["result"])
				lifecycleCheck(t, verbose["finished_at"] != nil && verbose["gitdir"] != nil, "verbose finish missing full fields: %v", verbose)
				f.git("worktree", "remove", path)
			}

			for _, explicit := range [][2]string{{"pi", "launcher-owner"}, {"codex", ""}} {
				created := f.call("create", "--agent", explicit[0], "--owner-id", explicit[1])
				owner, present := created["owner_id"].(string)
				lifecycleCheck(t, created["agent"] == explicit[0] && owner == explicit[1] && present == (explicit[1] != ""), "explicit identity not retained: %v", created)
				f.call("finish", lifecycleString(t, created, "id"))
				f.git("worktree", "remove", lifecycleString(t, created, "path"))
			}

			for _, args := range [][]string{{"create", "--agent", "../escape"}, {"create", "--", "--verbose"}, {"finish", "--", "--verbose"}, {"finish"}} {
				failure := f.fail(append([]string{f.binary}, args...)...)
				errorObject := lifecycleObject(t, lifecycleJSON(t, failure.stdout)["error"])
				lifecycleCheck(t, errorObject["code"] == "invalid_arguments", "invalid input lost error envelope: %v", errorObject)
			}

			opaque := f.call("create", "--owner-id", "--verbose")
			lifecycleCheck(t, opaque["owner_id"] == "--verbose" && opaque["gitdir"] == nil, "opaque owner was parsed as flag: %v", opaque)
			f.call("finish", lifecycleString(t, opaque, "id"))
			f.git("worktree", "remove", lifecycleString(t, opaque, "path"))
		})
	}
}

func TestCallerConflicts(t *testing.T) {
	f := newFixture(t)
	lifecycleInit(f)
	f.env["CODEX_THREAD_ID"], f.env["CLAUDE_CODE_SESSION_ID"] = "codex-owner", "claude-owner"
	before := f.git("for-each-ref", "--format=%(refname)")
	rejected := lifecycleObject(t, lifecycleJSON(t, f.fail(f.binary, "create").stdout)["error"])
	lifecycleCheck(t, rejected["code"] == "invalid_arguments" && strings.Contains(fmt.Sprint(rejected["message"]), "multiple agent environments"), "ambiguous identity was not rejected: %v", rejected)
	lifecycleCheck(t, f.git("for-each-ref", "--format=%(refname)") == before, "ambiguous caller changed refs")

	for _, selector := range []string{"flag", "environment"} {
		args := []string{"create", "--agent", "claude"}

		if selector == "environment" {
			f.env["WORKTREE_AGENT"] = "claude"
			args = []string{"create"}
		}

		selected := f.call(args...)
		lifecycleCheck(t, selected["agent"] == "claude" && selected["owner_id"] == "claude-owner", "selected caller borrowed owner: %v", selected)
		f.call("finish", lifecycleString(t, selected, "id"))
		f.git("worktree", "remove", lifecycleString(t, selected, "path"))
	}

	f.env["WORKTREE_AGENT"] = "../escape"
	invalid := lifecycleObject(t, lifecycleJSON(t, f.fail(f.binary, "create").stdout)["error"])
	lifecycleCheck(t, invalid["code"] == "invalid_arguments", "invalid inferred slug was not rejected: %v", invalid)
}

func TestLifecycleProtectionAndReconciliation(t *testing.T) {
	f := newFixture(t)
	lifecycleInit(f)
	created := f.call("create", "--agent", "codex", "--owner-id", "full-launch-token")
	path, id := lifecycleString(t, created, "path"), lifecycleString(t, created, "id")
	lifecycleCheck(t, created["state"] == "active" && f.exists(path), "creation did not confirm checkout: %v", created)
	lifecycleCheck(t, path == filepath.Join(f.repo, ".worktrees", "codex-"+id[:8]) && len(id) == 32, "allocation identity mismatch: %v", created)
	lifecycleCheck(t, created["agent"] == "codex" && created["owner_id"] == "full-launch-token" && created["branch"] == "worktree/"+filepath.Base(path), "allocation metadata mismatch: %v", created)

	human := f.run(f.binary, "status").stdout
	lifecycleCheck(t, strings.Contains(human, "full-launch-token") && strings.Contains(human, lifecycleString(t, created, "branch")), "human status lost ownership: %s", human)

	linkedResult := f.execute(path, f.binary, "create", "--agent", "claude", "--json")
	lifecycleCheck(t, linkedResult.code == 0, "linked create failed: %s%s", linkedResult.stdout, linkedResult.stderr)
	linked := lifecycleObject(t, lifecycleJSON(t, linkedResult.stdout)["result"])
	linkedPath, linkedID := lifecycleString(t, linked, "path"), lifecycleString(t, linked, "id")
	lifecycleCheck(t, filepath.Dir(linkedPath) == filepath.Join(f.repo, ".worktrees") && strings.HasPrefix(lifecycleString(t, linked, "branch"), "worktree/claude-"), "linked allocation used another root: %v", linked)
	f.call("finish", linkedID)
	f.git("worktree", "remove", linkedPath)
	record := filepath.Join(f.env["XDG_STATE_HOME"], "worktree-manager", "tasks", linkedID+".json")
	legacy := lifecycleJSON(t, f.read(record))
	delete(legacy, "agent")

	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}

	f.write(record, string(legacyJSON))
	lifecycleCheck(t, lifecycleRow(t, f.call("status"), "tasks", "id", linkedID)["state"] == "finished", "legacy record became unreadable")
	f.gitAt(path, "branch", "-m", "task/first")
	observed := lifecycleRow(t, f.call("status"), "tasks", "id", id)
	lifecycleCheck(t, observed["observed_branch"] == "task/first" && observed["observed_path"] == path, "branch rename lost registration: %v", observed)
	f.gitAt(path, "checkout", "--detach")
	detached := lifecycleRow(t, f.call("status"), "tasks", "id", id)
	lifecycleCheck(t, detached["observation"] == "available" && detached["observed_branch"] == nil, "detached state not observed: %v", detached)

	displayFound := false

	for _, line := range strings.Split(f.run(f.binary, "status").stdout, "\n") {
		if strings.HasPrefix(line, id) {
			displayFound = true

			lifecycleCheck(t, !strings.Contains(line, lifecycleString(t, created, "branch")), "detached display shows original branch: %s", line)
		}
	}

	lifecycleCheck(t, displayFound, "human status omitted detached task %s", id)
	f.gitAt(path, "checkout", "task/first")
	before := f.git("for-each-ref", "--format=%(refname)")
	rejected := f.fail(f.binary, "create", "--branch", "task/first", "--json")
	lifecycleCheck(t, rejected.code == 2 && strings.Contains(rejected.stderr, "already exists"), "existing branch not rejected: %+v", rejected)
	errorObject := lifecycleObject(t, lifecycleJSON(t, rejected.stdout)["error"])
	lifecycleCheck(t, reflect.DeepEqual(errorObject, map[string]any{"code": "invalid_arguments", "outcome": "rejected", "message": "allocation rejected: branch already exists; create requires a fresh named branch"}), "branch collision error changed: %v", errorObject)
	lifecycleCheck(t, strings.Contains(f.fail(f.binary, "create", "--path", filepath.Join(f.repo, ".git/not-a-checkout")).stderr, "administrative"), "administrative path was not rejected")
	lifecycleCheck(t, f.git("for-each-ref", "--format=%(refname)") == before, "rejection changed refs")
	nativePath := filepath.Join(f.root, "native-tools")
	f.mkdir(nativePath)

	for _, name := range []string{"awk", "basename", "cat", "column", "cut", "date", "dirname", "du", "find", "git", "head", "id", "lsof", "ls", "mkdir", "mktemp", "mv", "od", "printf", "pwd", "readlink", "rm", "sed", "sleep", "sort", "stat", "tail", "tr", "uname", "wc", "xargs"} {
		tool, lookupErr := exec.LookPath(name)
		if lookupErr == nil {
			if linkErr := os.Symlink(tool, filepath.Join(nativePath, name)); linkErr != nil {
				t.Fatal(linkErr)
			}
		}
	}

	previousPath := f.env["PATH"]
	f.env["PATH"] = nativePath
	nativeReport := f.call("prune", "--all")
	lifecycleCheck(t, lifecycleRow(t, nativeReport, "worktrees", "path", path)["reason"] == "active task", "runtime-free prune lost protection")

	f.env["PATH"] = previousPath
	lifecycleCheck(t, lifecycleObject(t, f.call("status")["totals"])["linked_count"] == "1", "unexpected linked checkout total")
	lifecycleCheck(t, lifecycleRow(t, f.call("prune", "--all", "--force"), "worktrees", "path", path)["reason"] == "active task", "force bypassed task protection")
	f.git("worktree", "unlock", path)
	lifecycleCheck(t, lifecycleRow(t, f.call("prune", "--all", "--force"), "worktrees", "path", path)["reason"] == "active task", "manual unlock ended task protection")
	f.git("worktree", "lock", "--reason", "external owner", path)
	finished := f.call("finish", id)
	lifecycleCheck(t, strings.TrimSpace(f.read(filepath.Join(lifecycleString(t, created, "gitdir"), "locked"))) == "external owner", "finish changed another owner's lock")
	f.git("worktree", "unlock", path)
	lifecycleCheck(t, finished["state"] == "finished" && f.exists(path) && f.call("finish", id)["state"] == "finished", "finish removed checkout or was not idempotent")
	f.write(filepath.Join(path, "unfinished"), "keep me")
	lifecycleCheck(t, lifecycleRow(t, f.call("prune", "--all"), "worktrees", "path", path)["reason"] == "dirty", "finished dirty checkout was not retained")
	lifecycleRemove(f, filepath.Join(path, "unfinished"))
	lifecycleCheck(t, lifecycleRow(t, f.call("prune", "--all", "--apply"), "worktrees", "path", path)["outcome"] == "removed" && !f.exists(path), "eligible finished checkout not removed")
	f.git("show-ref", "--verify", "refs/heads/task/first")
	f.git("worktree", "add", "-b", "replacement-registration", path)
	historical := lifecycleRow(t, f.call("status"), "tasks", "id", id)
	lifecycleCheck(t, historical["observation"] != "available" && historical["observed_path"] == nil, "replacement checkout adopted old identity: %v", historical)
	f.git("worktree", "remove", path)
	f.call("limits", "--max-worktrees", "0")
	denied := f.fail(f.binary, "create", "--branch", "blocked", "--json")
	lifecycleCheck(t, lifecycleObject(t, lifecycleJSON(t, denied.stdout)["error"])["code"] == "budget_full" && strings.Contains(denied.stderr, "full"), "budget denial changed: %+v", denied)
	lifecycleCheck(t, f.execute(f.repo, f.gitBinary, "show-ref", "--verify", "refs/heads/blocked").code != 0, "budget denial created branch")
	f.call("limits", "--max-worktrees", "off")
	backfill := f.call("backfill", f.root)
	lifecycleCheck(t, backfill["complete"] == true && len(lifecycleRows(t, backfill["repositories"])) == 1, "backfill did not deduplicate repository: %v", backfill)
	lifecycleCheck(t, f.call("prune", f.repo)["outcome"] == "preview", "prune did not preview by default")
	forced := filepath.Join(f.root, "force-receipt")
	f.git("worktree", "add", "-b", "force-receipt", forced)
	f.write(filepath.Join(forced, "dirty"), "explicitly discard this fixture")
	lifecycleCheck(t, lifecycleRow(t, f.call("prune", "--force", "--apply", f.repo), "worktrees", "path", forced)["outcome"] == "removed" && !f.exists(forced), "explicit force removal not confirmed")
	f.call("limits", "--max-worktrees", "1")
	f.fail(f.binary, "limits", "--max-worktrees", "off", "--max-bytes", "invalid")
	lifecycleCheck(t, f.git("config", "--local", "--get", "worktree.limit") == "1", "invalid combined limits partially mutated policy")
	f.call("limits", "--max-worktrees", "off", "--max-bytes", "off")
	lifecycleCheck(t, f.call("limits", "--global", "--max-worktrees", "9007199254740993")["max_worktrees"] == "9007199254740993", "large limit lost precision")
	f.call("limits", "--global", "--max-worktrees", "off")
}

func TestLifecycleEligibilityAndNestedProtection(t *testing.T) {
	f := newFixture(t)
	lifecycleInit(f)
	changing := filepath.Join(f.root, "eligibility-changes")
	f.git("worktree", "add", "-b", "eligibility-changes", changing)
	dirty := filepath.Join(changing, "dirty")
	f.write(dirty, "becomes clean after preview")
	wrappers := filepath.Join(f.root, "wrappers")
	f.mkdir(wrappers)
	counter := filepath.Join(f.root, "lsof-counter")
	probe := filepath.Join(wrappers, "lsof")
	lifecycleExecutable(f, probe, "#!/bin/sh\nn=0\n[ ! -f "+lifecycleQuote(counter)+" ] || n=$(cat "+lifecycleQuote(counter)+")\nn=$((n+1))\nprintf '%s\\n' \"$n\" > "+lifecycleQuote(counter)+"\nif [ \"$n\" -eq 2 ]; then rm -f "+lifecycleQuote(dirty)+"; fi\nexit 0\n")
	previousPath := f.env["PATH"]
	f.env["PATH"] = wrappers + string(os.PathListSeparator) + previousPath
	report := f.call("prune", f.repo, "--apply")
	row := lifecycleRow(t, report, "worktrees", "path", changing)
	lifecycleCheck(t, row["outcome"] == "not_attempted" && f.exists(changing) && !f.exists(dirty), "changed eligibility was not retained: %v", row)
	f.env["PATH"] = previousPath
	f.git("worktree", "remove", changing)

	for _, movedOtherRepo := range []bool{false, true} {
		name := "outer"
		if movedOtherRepo {
			name = "outer-moved"
		}

		outer := filepath.Join(f.root, name)
		f.git("worktree", "add", "-b", name, outer)
		source := f.repo

		initial := filepath.Join(outer, "nested")
		if movedOtherRepo {
			source = filepath.Join(f.root, "other-repository")
			f.project(source, "main")
			initial = filepath.Join(f.root, "before-nesting")
		}

		created := f.execute(source, f.binary, "create", "--branch", "task/"+name, "--path", initial, "--json")
		lifecycleCheck(t, created.code == 0, "nested task allocation failed: %+v", created)
		nestedTask := lifecycleObject(t, lifecycleJSON(t, created.stdout)["result"])

		nested := filepath.Join(outer, "nested")
		if movedOtherRepo {
			f.gitAt(source, "worktree", "move", "--force", "--force", initial, nested)
		}

		valuable := filepath.Join(nested, "uncommitted")
		f.write(valuable, "retain this active task's work\n")
		lifecycleExecutable(f, probe, "#!/bin/sh\nexit 0\n")
		f.env["PATH"] = wrappers + string(os.PathListSeparator) + previousPath
		row = lifecycleRow(t, f.call("prune", "--worktree", outer, "--force", "--apply"), "worktrees", "path", outer)
		lifecycleCheck(t, row["reason"] == "active task" && row["outcome"] == "not_attempted" && f.read(valuable) == "retain this active task's work\n", "ancestor prune lost nested task: %v", row)
		f.env["PATH"] = previousPath
		f.call("finish", lifecycleString(t, nestedTask, "id"))
		lifecycleRemove(f, valuable)
		f.gitAt(source, "worktree", "remove", nested)
		f.git("worktree", "remove", outer)
	}
}

func TestLifecyclePartialOutcomes(t *testing.T) {
	f := newFixture(t)
	lifecycleInit(f)
	beforeMove, afterMove := filepath.Join(f.root, "before-move"), filepath.Join(f.root, "after-move")
	hook := filepath.Join(f.repo, ".git/hooks/post-checkout")
	prefix := "#!/bin/sh\nunset GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE GIT_INDEX_FILE\n"
	lifecycleExecutable(f, hook, prefix+"git -C "+lifecycleQuote(f.repo)+" worktree move --force --force "+lifecycleQuote(beforeMove)+" "+lifecycleQuote(afterMove)+"\n")
	moved := f.call("create", "--branch", "moved-task", "--path", beforeMove)
	lifecycleCheck(t, moved["state"] == "active" && moved["path"] == afterMove, "hook-moved allocation not reconciled: %v", moved)

	secondMove := filepath.Join(f.root, "second-move")
	f.git("worktree", "move", "--force", "--force", afterMove, secondMove)
	lifecycleCheck(t, lifecycleRow(t, f.call("status"), "tasks", "id", moved["id"])["observed_path"] == secondMove, "later move lost registration")
	f.call("finish", secondMove)
	f.git("worktree", "remove", secondMove)
	vanished := filepath.Join(f.root, "vanished")
	f.env["CODEX_THREAD_ID"] = "calling-agent-id"
	lifecycleExecutable(f, hook, prefix+"git -C "+lifecycleQuote(f.repo)+" worktree remove --force --force "+lifecycleQuote(vanished)+"\n")
	missing := f.callFail("create", "--branch", "vanished-task", "--path", vanished)
	lifecycleKeys(t, missing, "id", "path", "branch", "state", "agent", "owner_id", "error")
	lifecycleCheck(t, missing["state"] == "uncertain" && !f.exists(vanished), "vanished task was not uncertain: %v", missing)
	f.call("finish", lifecycleString(t, missing, "id"))
	lifecycleExecutable(f, hook, "#!/bin/sh\nexit 7\n")
	partial := f.callFail("create", "--branch", "hook-failure")
	lifecycleKeys(t, partial, "id", "path", "branch", "state", "agent", "owner_id", "error")
	lifecycleCheck(t, partial["state"] == "active" && f.exists(lifecycleString(t, partial, "path")) && strings.Contains(fmt.Sprint(partial["error"]), "exit status 7"), "hook failure lost surviving checkout evidence: %v", partial)
}
