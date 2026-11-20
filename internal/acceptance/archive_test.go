//go:build integration

package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestArchiveCleanup(t *testing.T) {
	f := newFixture(t)
	lifecycleInit(f)
	f.mkdir(f.env["CODEX_HOME"])
	database := filepath.Join(f.env["CODEX_HOME"], "state_5.sqlite")
	f.run("sqlite3", database, "CREATE TABLE threads (id TEXT, cwd TEXT, archived INTEGER, archived_at INTEGER, updated_at INTEGER);")
	created := lifecycleObject(t, lifecycleJSON(t, f.run(f.binary, "create", "--agent", "codex", "--owner-id", "archive-owner", "--json").stdout)["result"])
	path := lifecycleString(t, created, "path")
	id := lifecycleString(t, created, "id")
	branch := lifecycleString(t, created, "branch")

	f.write(filepath.Join(path, "tracked"), "unmerged committed work\n")
	f.gitAt(path, "add", "tracked")
	f.gitAt(path, "commit", "-qm", "keep unmerged work")

	now := time.Now().Unix()
	f.run("sqlite3", database, fmt.Sprintf("INSERT INTO threads VALUES ('archive-owner',%s,1,%d,%d);", sqlLiteral(f.repo), now, now))
	// Default disabled, and a configured delay retains protection.
	f.run(f.binary, "cleanup-archived", "--apply", "--json")
	f.fail(f.binary, "internal-task-check", path)
	f.write(f.env["WORKTREE_CONFIG"], `{"archive_cleanup":{"enabled":true,"grace_period":"7d"}}`)
	f.run(f.binary, "cleanup-archived", "--apply", "--json")
	f.fail(f.binary, "internal-task-check", path)
	f.write(f.env["WORKTREE_CONFIG"], `{"archive_cleanup":{"enabled":true,"grace_period":"0"}}`)
	f.run(f.binary, "cleanup-archived", "--json")
	f.fail(f.binary, "internal-task-check", path)
	// Dirty work survives automatic retirement and can be recovered on the retained branch.
	f.write(filepath.Join(path, "valuable"), "keep my work\n")
	f.run(f.binary, "cleanup-archived", "--apply", "--json")
	//nolint:gosec // rationale: Read the test-owned allocated checkout to verify preservation.
	if data, err := os.ReadFile(filepath.Join(path, "valuable")); err != nil || string(data) != "keep my work\n" {
		t.Fatalf("lost dirty work: %q %v", data, err)
	}

	f.run("sqlite3", database, "ALTER TABLE threads RENAME TO unavailable;")
	f.fail(f.binary, "cleanup-archived", "--apply", "--json")
	f.fail(f.binary, "internal-task-check", path)
	f.run("sqlite3", database, "ALTER TABLE unavailable RENAME TO threads;")
	f.run("sqlite3", database, "UPDATE threads SET archived=0;")
	f.fail(f.binary, "internal-task-check", path)
	f.run(f.binary, "prune", "--worktree", path, "--apply", "--json")
	f.run("sqlite3", database, "UPDATE threads SET archived=1;")
	// An active sibling session also protects the checkout.
	f.run("sqlite3", database, fmt.Sprintf("INSERT INTO threads VALUES ('sibling',%s,0,NULL,%d);", sqlLiteral(path), now))
	f.fail(f.binary, "internal-task-check", path)
	moved := filepath.Join(f.repo, ".worktrees", "moved")
	f.git("worktree", "move", path, moved)
	f.run("sqlite3", database, fmt.Sprintf("UPDATE threads SET cwd=%s WHERE id='sibling';", sqlLiteral(moved)))
	f.fail(f.binary, "internal-task-check", moved)
	// A replacement registration at the old path must never be authorized by this task.
	f.git("worktree", "add", "-qb", "replacement", path, "main")
	f.fail(f.binary, "internal-archive-prune", id, path)
	f.run(f.binary, "cleanup-archived", "--apply", "--json")
	f.git("worktree", "remove", path)
	f.git("worktree", "move", moved, path)
	f.run("sqlite3", database, "DELETE FROM threads WHERE id='sibling';")
	lifecycleRemove(f, filepath.Join(path, "valuable"))
	f.write(filepath.Join(f.repo, ".git", "info", "exclude"), "private\n")
	f.write(filepath.Join(path, "private"), "valuable ignored artifact\n")
	f.run(f.binary, "cleanup-archived", "--apply", "--json")

	if _, err := os.Stat(filepath.Join(path, "private")); err != nil {
		t.Fatal(err)
	}

	lifecycleRemove(f, filepath.Join(path, "private"))

	report := f.run(f.binary, "cleanup-archived", "--apply", "--json").stdout
	if !strings.Contains(report, `"outcome":"removed"`) {
		t.Fatalf("expected confirmed removal: %s", report)
	}

	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("checkout remains: %v", err)
	}

	f.git("show-ref", "--verify", "refs/heads/"+branch)
	lifecycleCheck(t, f.git("show", branch+":tracked") == "unmerged committed work", "unmerged commit lost")
	finished := lifecycleObject(t, lifecycleJSON(t, f.run(f.binary, "finish", id, "--json").stdout)["result"])
	lifecycleCheck(t, finished["state"] == "finished", "unexpected state: %v", finished)
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func TestArchiveWatcher(t *testing.T) {
	f := newFixture(t)
	f.mkdir(f.env["CODEX_HOME"])
	f.run("sqlite3", filepath.Join(f.env["CODEX_HOME"], "state_5.sqlite"), "CREATE TABLE threads (id TEXT, cwd TEXT, archived INTEGER, archived_at INTEGER, updated_at INTEGER);")

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	//nolint:gosec // rationale: Exercise the test-built executable and fixed watcher arguments.
	child := exec.CommandContext(ctx, f.binary, "cleanup-archived", "--apply", "--watch", "10ms", "--json")

	child.Dir = f.repo
	for key, value := range f.env {
		child.Env = append(child.Env, key+"="+value)
	}

	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	if err = child.Start(); err != nil {
		t.Fatal(err)
	}

	defer func() {
		cancel()

		if child.ProcessState == nil {
			if waitErr := child.Wait(); waitErr != nil && !t.Failed() {
				t.Errorf("watcher cleanup: %v", waitErr)
			}
		}
	}()

	decoder := json.NewDecoder(stdout)

	var event map[string]any
	if err = decoder.Decode(&event); err != nil {
		t.Fatal(err)
	}

	lifecycleCheck(t, lifecycleObject(t, event["result"])["enabled"] == false, "default watcher unexpectedly enabled: %v", event)
	f.write(f.env["WORKTREE_CONFIG"], `{"archive_cleanup":{"enabled":true,"grace_period":"0"}}`)

	for {
		if err = decoder.Decode(&event); err != nil {
			t.Fatal(err)
		}

		if lifecycleObject(t, event["result"])["enabled"] == true {
			break
		}
	}

	if err = child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err = child.Wait(); err != nil {
		t.Fatal(err)
	}
}
