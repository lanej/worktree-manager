//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

type result struct {
	stdout, stderr string
	code           int
}

type fixture struct {
	t                                   *testing.T
	root, repo, home, binary, gitBinary string
	env                                 map[string]string
}

func projectRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	return root
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	binary := os.Getenv("WORKTREE_BINARY")
	if binary == "" {
		t.Fatal("run scripts/test.sh to build the acceptance executable")
	}

	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	f := &fixture{t: t, root: root, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home with 'quotes"), binary: binary, gitBinary: git, env: map[string]string{}}

	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "WORKTREE_") {
			continue
		}

		f.env[key] = value
	}

	for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE", "PI_SESSION_ID", "PI_CODING_AGENT", "GEMINI_CLI", "OPENCODE_SESSION_ID", "OPENCODE", "AI_AGENT"} {
		delete(f.env, key)
	}

	for key, value := range map[string]string{
		"HOME": f.home, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull, "GIT_TERMINAL_PROMPT": "0",
		"GIT_AUTHOR_DATE": "2000-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2000-01-01T00:00:00Z",
		"NO_COLOR": "1", "XDG_STATE_HOME": filepath.Join(root, "state[1]"), "XDG_CACHE_HOME": filepath.Join(root, "cache"),
		"XDG_CONFIG_HOME": filepath.Join(root, "config"), "CODEX_HOME": filepath.Join(root, "codex"),
		"WORKTREE_BINARY": binary, "WORKTREE_CONFIG": filepath.Join(root, "worktree-config.json"),
	} {
		f.env[key] = value
	}

	f.mkdir(f.repo)
	f.mkdir(f.home)
	f.write(f.env["WORKTREE_CONFIG"], `{"recovery_allowed_github_owners":["easypost-sandbox"]}`)

	return f
}

func (f *fixture) execute(cwd string, args ...string) result {
	f.t.Helper()

	ctx, cancel := context.WithTimeout(f.t.Context(), 180*time.Second)
	defer cancel()

	program := args[0]
	if !strings.ContainsRune(program, filepath.Separator) {
		program = ""

		for _, directory := range filepath.SplitList(f.env["PATH"]) {
			candidate := filepath.Join(directory, args[0])
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				program = candidate
				break
			}
		}

		if program == "" {
			return result{code: 127, stderr: "command not found in fixture PATH: " + args[0]}
		}
	}

	//nolint:gosec // rationale: The acceptance cases select explicit fixture tools and argv; process effects are the boundary under test.
	command := exec.CommandContext(ctx, program, args[1:]...)
	command.Dir = cwd

	for key, value := range f.env {
		command.Env = append(command.Env, key+"="+value)
	}

	sort.Strings(command.Env)

	var stdout, stderr bytes.Buffer

	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0

	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			f.t.Fatalf("%q: %v", args, err)
		}

		code = exit.ExitCode()
	}

	if ctx.Err() != nil {
		f.t.Fatalf("%q timed out: %s%s", args, stdout.String(), stderr.String())
	}

	return result{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func (f *fixture) run(args ...string) result {
	f.t.Helper()

	r := f.execute(f.repo, args...)
	if r.code != 0 {
		f.t.Fatalf("%q: exit %d\n%s%s", args, r.code, r.stdout, r.stderr)
	}

	return r
}

func (f *fixture) fail(args ...string) result {
	f.t.Helper()

	r := f.execute(f.repo, args...)
	if r.code == 0 {
		f.t.Fatalf("%q unexpectedly succeeded: %s%s", args, r.stdout, r.stderr)
	}

	return r
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()

	return f.gitAt(f.repo, args...)
}

func (f *fixture) gitAt(cwd string, args ...string) string {
	f.t.Helper()

	argv := append([]string{f.gitBinary, "-c", "user.name=Acceptance", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)

	r := f.execute(cwd, argv...)
	if r.code != 0 {
		f.t.Fatalf("git %q: exit %d\n%s%s", args, r.code, r.stdout, r.stderr)
	}

	return strings.TrimSpace(r.stdout)
}

func (f *fixture) call(args ...string) map[string]any {
	f.t.Helper()

	return f.decodeResult(f.run(append([]string{f.binary}, append(args, "--json")...)...))
}

func (f *fixture) callFail(args ...string) map[string]any {
	f.t.Helper()

	return f.decodeResult(f.fail(append([]string{f.binary}, append(args, "--json")...)...))
}

func (f *fixture) decodeResult(r result) map[string]any {
	f.t.Helper()

	var envelope struct {
		Result map[string]any `json:"result"`
	}

	if err := json.Unmarshal([]byte(r.stdout), &envelope); err != nil || envelope.Result == nil {
		f.t.Fatalf("invalid result: %v\n%s%s", err, r.stdout, r.stderr)
	}

	return envelope.Result
}

func (f *fixture) project(path, branch string) {
	f.t.Helper()
	f.mkdir(path)
	f.gitAt(path, "init", "-q", "--initial-branch="+branch)
	f.write(filepath.Join(path, ".gitignore"), ".worktrees/\nnode_modules/\nevidence/\nvendor/\n")
	f.gitAt(path, "add", ".gitignore")
	f.gitAt(path, "commit", "-qm", "initial")
	f.gitAt(path, "update-ref", "refs/remotes/origin/"+branch, "HEAD")
	f.gitAt(path, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
}

func (f *fixture) mkdir(path string) {
	f.t.Helper()

	if err := os.MkdirAll(path, 0o700); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) write(path, content string) {
	f.t.Helper()
	f.mkdir(filepath.Dir(path))

	// #nosec G703 -- Acceptance cases supply paths inside their test-owned fixture directories.
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(path string) string {
	f.t.Helper()

	//nolint:gosec // rationale: Read the explicit fixture or repository artifact selected by the acceptance case.
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}

	return string(data)
}

func (f *fixture) exists(path string) bool {
	f.t.Helper()

	_, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}

	return err == nil
}
