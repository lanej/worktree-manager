//go:build integration

package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWIPPushPreservesRemoteSnapshots(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.root, "src")
	f.mkdir(root)
	repo, denied := filepath.Join(root, "sandbox"), filepath.Join(root, "personal")
	remote := filepath.Join(f.root, "remote.git")
	checkout := filepath.Join(repo, ".worktrees/unfinished task")
	privateCheckout := filepath.Join(denied, ".worktrees/private")
	approvedURL := "https://github.com/easypost-sandbox/cleanup-fixture.git"
	calls, editDuringPush := filepath.Join(f.root, "push-calls"), filepath.Join(f.root, "edit-during-push")
	wrappers := filepath.Join(f.root, "wrappers")
	f.mkdir(wrappers)
	f.env["PATH"] = wrappers + string(os.PathListSeparator) + f.env["PATH"]
	f.env["WORKTREE_TEST_GIT"], f.env["WORKTREE_TEST_URL"] = f.gitBinary, approvedURL
	f.env["WORKTREE_TEST_REMOTE"], f.env["WORKTREE_TEST_CALLS"] = remote, calls
	f.env["WORKTREE_TEST_EDIT"], f.env["WORKTREE_TEST_CHECKOUT"] = editDuringPush, checkout
	delete(f.env, "GIT_AUTHOR_DATE")
	delete(f.env, "GIT_COMMITTER_DATE")

	gitWrapper := filepath.Join(wrappers, "git")
	f.write(gitWrapper, `#!/bin/bash
args=("$@")
command=${args[0]}
if [ "$command" = -C ]; then command=${args[2]}; fi
url_index=-1
get_url=false
for ((i=0; i<${#args[@]}; i++)); do
    if [ "${args[$i]}" = "$WORKTREE_TEST_URL" ]; then url_index=$i; fi
    if [ "${args[$i]}" = --get-url ]; then get_url=true; fi
done
if { [ "$command" = push ] || [ "$command" = ls-remote ]; } && ! $get_url && [ "$url_index" -ge 0 ]; then
    args[$url_index]=$WORKTREE_TEST_REMOTE
    if [ "$command" = push ]; then
        printf '%s\n' "${args[*]}" >> "$WORKTREE_TEST_CALLS"
        "$WORKTREE_TEST_GIT" "${args[@]}"
        status=$?
        if [ "$status" -eq 0 ] && [ -f "$WORKTREE_TEST_EDIT" ]; then
            printf 'work created while publishing\n' > "$WORKTREE_TEST_CHECKOUT/late.txt"
            rm "$WORKTREE_TEST_EDIT"
        fi
        exit "$status"
    fi
fi
exec "$WORKTREE_TEST_GIT" "${args[@]}"
`)

	//nolint:gosec // rationale: The task-owned transport fixture must be executable by cleanup's Git subprocess.
	if err := os.Chmod(gitWrapper, 0o755); err != nil {
		t.Fatal(err)
	}

	oldGit := func(directory string, args ...string) string {
		f.env["GIT_AUTHOR_DATE"], f.env["GIT_COMMITTER_DATE"] = "2000-01-01T00:00:00Z", "2000-01-01T00:00:00Z"
		output := f.gitAt(directory, args...)
		delete(f.env, "GIT_AUTHOR_DATE")
		delete(f.env, "GIT_COMMITTER_DATE")

		return output
	}
	project := func(directory string) {
		f.mkdir(directory)
		f.gitAt(directory, "init", "-q", "--initial-branch", "trunk")
		f.gitAt(directory, "config", "user.name", "WIP cleanup check")
		f.gitAt(directory, "config", "user.email", "cleanup@example.invalid")
		f.gitAt(directory, "config", "commit.gpgsign", "false")
		f.write(filepath.Join(directory, ".gitignore"), ".worktrees/\nnode_modules/\n")
		f.write(filepath.Join(directory, "source.txt"), "original source\n")
		f.write(filepath.Join(directory, "removed.txt"), "remove in WIP\n")
		f.gitAt(directory, "add", ".")
		oldGit(directory, "commit", "-qm", "Initialize WIP fixture")
	}
	scan := func(status int, args ...string) string {
		command := append([]string{f.binary, "legacy-cleanup"}, args...)
		command = append(command, root)

		out := f.execute(f.root, command...)
		if out.code != status {
			t.Fatalf("cleanup status %d, want %d: %s%s", out.code, status, out.stdout, out.stderr)
		}

		return out.stdout + out.stderr
	}
	contains := func(output string, values ...string) {
		for _, value := range values {
			if !strings.Contains(output, value) {
				t.Fatalf("missing %q in %s", value, output)
			}
		}
	}

	project(repo)
	project(denied)
	f.gitAt(f.root, "init", "-q", "--bare", remote)
	f.gitAt(repo, "remote", "add", "origin", approvedURL)
	f.gitAt(denied, "remote", "add", "origin", approvedURL)
	f.gitAt(denied, "remote", "set-url", "--push", "origin", "https://github.com/personal-owner/private-fixture.git")
	f.gitAt(repo, "worktree", "add", "--detach", checkout)
	f.gitAt(denied, "worktree", "add", "--detach", privateCheckout)
	f.write(filepath.Join(privateCheckout, "private.txt"), "leave this work alone\n")
	f.write(filepath.Join(checkout, "unmerged.txt"), "detached committed work\n")
	f.gitAt(checkout, "add", "unmerged.txt")
	oldGit(checkout, "commit", "-qm", "Unmerged work")
	originalHead := f.gitAt(checkout, "rev-parse", "HEAD")
	f.write(filepath.Join(checkout, "source.txt"), "unfinished source\n")
	f.write(filepath.Join(checkout, "new.txt"), "untracked source\n")

	if err := os.Remove(filepath.Join(checkout, "removed.txt")); err != nil {
		t.Fatal(err)
	}

	f.mkdir(filepath.Join(checkout, "node_modules"))
	f.write(filepath.Join(checkout, "node_modules/disposable"), strings.Repeat("x", 65536))
	originalStatus := f.gitAt(checkout, "status", "--porcelain")
	preview := scan(0, "--wip-push", "--min-size", "1KiB")
	contains(preview, "PUSH TO", "wip-push", "commit WIP", "no approved push remote", "1 worktree(s) would be removed")

	if strings.Count(preview, "DISK USAGE") != 1 || f.gitAt(checkout, "rev-parse", "HEAD") != originalHead || f.gitAt(checkout, "status", "--porcelain") != originalStatus || f.gitAt(repo, "for-each-ref", "refs/heads/wip/cleanup/") != "" || f.exists(calls) {
		t.Fatalf("preview mutated work or emitted duplicate headers: %s", preview)
	}

	incompatible := scan(1, "--wip-push", "--accept-ignored", "-y")
	contains(incompatible, "--accept-ignored", "exclude --wip-push")

	if f.exists(calls) || f.gitAt(checkout, "rev-parse", "HEAD") != originalHead {
		t.Fatal("incompatible flags attempted publication")
	}

	f.gitAt(denied, "remote", "set-url", "--push", "origin", "shortcut:cleanup-fixture.git")
	f.gitAt(denied, "config", "url.https://github.com/easypost-sandbox/.insteadOf", "shortcut:")
	f.gitAt(denied, "config", "url.https://github.com/personal-owner/.insteadOf", "https://github.com/easypost-sandbox/")

	rewritten := scan(0, "--wip-push")
	contains(rewritten, "push URL rewrites elsewhere", "1 worktree(s) would be removed")

	if f.exists(calls) {
		t.Fatal("preview attempted a rewritten push")
	}

	rejection := filepath.Join(remote, "hooks/pre-receive")
	f.write(rejection, "#!/bin/sh\nexit 1\n")

	//nolint:gosec // rationale: Git executes this task-owned fixture hook to reject the test push.
	if err := os.Chmod(rejection, 0o755); err != nil {
		t.Fatal(err)
	}

	failed := scan(1, "--wip-push", "-y")
	contains(failed, "WIP push failed", "Removed 0 worktree(s)")

	firstBranch := f.gitAt(checkout, "symbolic-ref", "--short", "HEAD")

	firstHead := f.gitAt(checkout, "rev-parse", "HEAD")
	if !f.exists(checkout) || !strings.HasPrefix(firstBranch, "wip/cleanup/") || firstHead == originalHead || f.gitAt(checkout, "status", "--porcelain") != "" {
		t.Fatal("failed push did not retain the committed recovery checkout")
	}

	if f.gitAt(checkout, "show", "HEAD:source.txt") != "unfinished source" || f.gitAt(checkout, "show", "HEAD:new.txt") != "untracked source" || strings.Contains(f.gitAt(checkout, "ls-tree", "--name-only", "HEAD"), "removed.txt") || f.gitAt(remote, "for-each-ref", "refs/heads/") != "" {
		t.Fatal("failed publication lost WIP contents or created remote refs")
	}

	if err := os.Remove(rejection); err != nil {
		t.Fatal(err)
	}

	f.write(editDuringPush, "")

	changed := scan(1, "--wip-push", "--accept-recent", "-y")
	contains(changed, "Retaining", "dirty")

	if !f.exists(checkout) || !f.exists(filepath.Join(checkout, "late.txt")) {
		t.Fatal("work created during publication was removed")
	}

	publishedBranch := f.gitAt(checkout, "symbolic-ref", "--short", "HEAD")
	if f.gitAt(remote, "rev-parse", "refs/heads/"+publishedBranch) != firstHead {
		t.Fatal("confirmed remote ref does not retain the first WIP snapshot")
	}

	finished := scan(0, "--wip-push", "-a", "0", "-y")
	contains(finished, "Removed 1 worktree(s)")

	if f.exists(checkout) || strings.Contains(f.gitAt(repo, "worktree", "list", "--porcelain"), checkout) {
		t.Fatal("confirmed publication did not remove the checkout")
	}

	branches := strings.Fields(f.gitAt(remote, "for-each-ref", "--format=%(refname)", "refs/heads/"))
	if len(branches) != 2 {
		t.Fatalf("remote recovery branches: %v", branches)
	}

	finalRef := ""

	for _, branch := range branches {
		if !strings.HasSuffix(branch, publishedBranch) {
			finalRef = branch
		}
	}

	for file, expected := range map[string]string{"late.txt": "work created while publishing", "unmerged.txt": "detached committed work", "source.txt": "unfinished source", "new.txt": "untracked source"} {
		if f.gitAt(remote, "show", finalRef+":"+file) != expected {
			t.Fatalf("remote snapshot lost %s", file)
		}
	}

	if strings.Contains(f.gitAt(remote, "ls-tree", "--name-only", finalRef), "removed.txt") || f.gitAt(repo, "rev-parse", finalRef) != f.gitAt(remote, "rev-parse", finalRef) || f.gitAt(repo, "rev-parse", "refs/heads/"+firstBranch) != firstHead {
		t.Fatal("recovery refs or removed-file snapshot changed")
	}

	if !f.exists(privateCheckout) || f.read(filepath.Join(privateCheckout, "private.txt")) != "leave this work alone\n" || len(strings.Split(strings.TrimSpace(f.read(calls)), "\n")) != 3 || f.gitAt(repo, "status", "--porcelain") != "" {
		t.Fatal("publication count, private checkout preservation, or primary cleanliness changed")
	}
}
