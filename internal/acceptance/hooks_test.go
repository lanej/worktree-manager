//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfiguredHooks(t *testing.T) {
	git := os.Getenv("WORKTREE_HOOKS_GIT")
	if git == "" {
		t.Skip("WORKTREE_HOOKS_GIT is not set; configured-hook acceptance requires the patched Git runtime")
	}

	git, err := filepath.Abs(git)
	if err != nil {
		t.Fatal(err)
	}

	f := newFixture(t)
	installed := filepath.Join(f.home, "bin # quote's\\backslash", "work tree's")
	f.mkdir(filepath.Dir(installed))
	installCopy(t, f.binary, installed)
	f.binary, f.gitBinary = installed, git
	f.env["WORKTREE_BINARY"] = installed
	f.env["PATH"] = filepath.Dir(git) + string(os.PathListSeparator) + f.env["PATH"]
	config := filepath.Join(f.home, "hooks.gitconfig")
	f.env["GIT_CONFIG_GLOBAL"] = config
	f.run(f.binary, "hooks", "--file", config)
	printed := filepath.Join(f.home, "printed.gitconfig")
	f.write(printed, f.run(f.binary, "hooks").stdout)

	for _, event := range []string{"pre", "post", "checkout"} {
		key := "hook.worktree-manager-" + event + ".command"
		explicit := f.run(git, "config", "--file", config, "--get", key).stdout
		lifecycleCheck(t, f.run(git, "config", "--file", printed, "--get", key).stdout == explicit, "printed hook differs from written hook: %s", event)
	}

	f.env["GIT_CONFIG_GLOBAL"] = printed
	f.git("init", "-q", "--initial-branch=main")
	f.git("commit", "--allow-empty", "-qm", "initial")
	custom := filepath.Join(f.root, "custom-hooks")
	f.mkdir(custom)
	log := filepath.Join(f.root, "hook-log")
	lifecycleExecutable(f, filepath.Join(custom, "post-checkout"), "#!/bin/sh\nprintf 'called\\n' >> "+lifecycleQuote(log)+"\n")
	f.git("config", "core.hooksPath", custom)
	first, second := filepath.Join(f.root, "first"), filepath.Join(f.root, "second")
	f.git("worktree", "add", "-b", "first", first)
	lifecycleCheck(t, f.read(log) == "called\n", "custom post-checkout hook not called once")
	f.git("worktree", "add", "--no-checkout", "-b", "second", second)
	report := f.call("status")
	lifecycleCheck(t, report["complete"] == true && lifecycleObject(t, report["totals"])["linked_count"] == "2", "external worktrees were not tracked: %v", report)
	f.call("limits", "--max-worktrees", "2")
	blocked := filepath.Join(f.root, "blocked")
	f.git("commit", "--allow-empty", "-qm", "advance")
	originalBranch := f.git("rev-parse", "first")
	denied := f.fail(git, "worktree", "add", "--force", "-B", "first", blocked)
	lifecycleCheck(t, strings.Contains(denied.stderr, "refusing add") && !f.exists(blocked), "pre-worktree hook did not veto allocation: %+v", denied)
	lifecycleCheck(t, f.git("rev-parse", "first") == originalBranch, "vetoed -B allocation reset existing branch")
	f.git("worktree", "remove", second)
	inventory := lifecycleJSON(t, f.read(filepath.Join(f.repo, ".git/worktree-policy/inventory.json")))
	lifecycleCheck(t, inventory["linked_count"] == float64(1), "removal hook did not refresh inventory: %v", inventory)
	f.git("worktree", "add", "--no-checkout", "-b", "third", filepath.Join(f.root, "third"))
	lifecycleCheck(t, f.read(log) == "called\n", "no-checkout allocation unexpectedly ran custom checkout hook")
}
