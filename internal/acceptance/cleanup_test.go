//go:build integration

package acceptance

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func cleanupCopyEnv(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for name, value := range input {
		result[name] = value
	}

	return result
}

func cleanupEnv(input map[string]string) []string {
	result := make([]string, 0, len(input))
	for name, value := range input {
		result = append(result, name+"="+value)
	}

	sort.Strings(result)

	return result
}

func cleanupRemove(t *testing.T, path string) {
	t.Helper()

	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

func cleanupChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func cleanupStatus(t *testing.T, output, path string) string {
	t.Helper()
	header := cleanupHeader(t, output)
	line := cleanupLine(t, output, path)
	start, end := strings.Index(header, "STATUS"), strings.Index(header, "REASON")
	cleanupRequire(t, start >= 0 && end > start && len(line) >= end, "invalid status table: %s", output)

	return strings.TrimSpace(line[start:end])
}

func TestCleanupSweep(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.root, "src")
	app := filepath.Join(root, "group/team app")
	library := filepath.Join(f.root, "z-library-primary")
	project := func(path, branch string) {
		f.project(path, branch)
		f.gitAt(path, "update-ref", "refs/remotes/origin/"+branch, "HEAD")
		f.gitAt(path, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+branch)
	}
	project(app, "main")
	project(library, "trunk")

	nested := filepath.Join(app, "vendor/nested-project")
	project(nested, "main")

	add := func(repo, path string, args ...string) {
		f.gitAt(repo, append([]string{"worktree", "add"}, append(args, path)...)...)
	}
	nestedCheckout := filepath.Join(nested, ".worktrees/retained")
	add(nested, nestedCheckout, "--detach")

	disposable := filepath.Join(app, ".worktrees/merged")
	add(app, disposable, "-b", "completed-task")
	pooled := filepath.Join(app, ".worktrees/.treehouse/app-fixture/1/app-1")
	add(app, pooled, "--detach")
	f.mkdir(filepath.Join(disposable, "node_modules"))
	cleanupPayload(t, filepath.Join(disposable, "node_modules/disposable-cache"), 2*1024*1024)
	dirty := filepath.Join(app, ".worktrees/unfinished")
	add(app, dirty, "--detach")
	f.write(filepath.Join(dirty, ".gitignore"), "unfinished edit\n")
	missing := filepath.Join(app, ".worktrees/abandoned")
	add(app, missing, "--detach")
	cleanupRemove(t, missing)

	hidden := filepath.Join(app, ".worktrees/hidden-index")
	add(app, hidden, "--detach")
	f.gitAt(hidden, "update-index", "--assume-unchanged", ".gitignore")
	f.write(filepath.Join(hidden, ".gitignore"), "edit hidden from git status\n")
	cleanupRequire(t, f.gitAt(hidden, "status", "--porcelain") == "", "assume unchanged fixture visible")

	evidence := filepath.Join(app, ".worktrees/evidence")
	add(app, evidence, "--detach")
	f.mkdir(filepath.Join(evidence, "evidence"))
	f.write(filepath.Join(evidence, "evidence/report.txt"), "retain ignored evidence")
	locked := filepath.Join(app, ".worktrees/pinned")
	add(app, locked, "--detach")
	f.gitAt(app, "worktree", "lock", "--reason", "Keep this checkout", locked)
	f.gitAt(app, "commit", "--allow-empty", "-qm", "Advance the merged base without file changes")
	f.gitAt(app, "update-ref", "refs/remotes/origin/main", "HEAD")
	operation := filepath.Join(app, ".worktrees/unfinished-merge")
	f.gitAt(app, "worktree", "add", "-b", "merge-in-progress", operation, "HEAD~1")
	f.gitAt(operation, "merge", "--no-ff", "--no-commit", "main")
	cleanupRequire(t, f.gitAt(operation, "status", "--porcelain") == "", "merge fixture dirty")

	dependencies := filepath.Join(root, "library linked checkout")
	add(library, dependencies, "--detach")
	f.mkdir(filepath.Join(dependencies, "node_modules"))
	cleanupPayload(t, filepath.Join(dependencies, "node_modules/disposable-cache"), 65536)
	completed := filepath.Join(library, ".worktrees/completed")
	add(library, completed, "--detach")
	active := filepath.Join(library, ".worktrees/background")
	add(library, active, "--detach")

	conflictNames := []string{"largest file.txt", "a\tfile.txt", "small-1.txt", "small-2.txt"}

	for _, name := range conflictNames {
		f.write(filepath.Join(app, name), "original\n")
	}

	f.write(filepath.Join(app, "binary.dat"), "\x00original")
	f.write(filepath.Join(app, "mode.txt"), "original\n")
	f.gitAt(app, "add", ".")
	f.gitAt(app, "commit", "-qm", "Initialize conflicting files")
	conflicted := filepath.Join(app, ".worktrees/conflicted")
	add(app, conflicted, "-b", "conflict-report")

	for _, name := range conflictNames {
		f.write(filepath.Join(conflicted, name), "head\n")
		f.write(filepath.Join(app, name), "base\n")
	}

	f.write(filepath.Join(conflicted, "largest file.txt"), strings.Repeat("head\n", 7))
	f.write(filepath.Join(app, "largest file.txt"), strings.Repeat("base\n", 2))
	f.write(filepath.Join(conflicted, "binary.dat"), "\x00head")
	f.write(filepath.Join(app, "binary.dat"), "\x00base")
	cleanupChmod(t, filepath.Join(conflicted, "mode.txt"), 0o755)
	f.gitAt(app, "rm", "-q", "mode.txt")
	f.gitAt(conflicted, "commit", "-qam", "Conflicting HEAD edits")
	f.gitAt(app, "commit", "-qam", "Conflicting base edits")
	f.gitAt(app, "update-ref", "refs/remotes/origin/main", "HEAD")
	conflictHead := f.gitAt(conflicted, "rev-parse", "HEAD")
	conflictIndex := f.read(f.gitAt(conflicted, "rev-parse", "--path-format=absolute", "--git-path", "index"))
	codexDirectory := filepath.Join(f.root, "codex")
	f.mkdir(codexDirectory)
	f.env["CODEX_HOME"] = codexDirectory

	codexAlias := filepath.Join(f.root, "codex-worktree-alias")
	if err := os.Symlink(app, codexAlias); err != nil {
		t.Fatal(err)
	}

	database := filepath.Join(codexDirectory, "state_5.sqlite")
	databaseBefore := ""

	hasSQLite := false
	if sqlite, err := exec.LookPath("sqlite3"); err == nil {
		hasSQLite = true
		sqlQuote := func(text string) string { return "'" + strings.ReplaceAll(text, "'", "''") + "'" }
		sql := "CREATE TABLE threads (id TEXT,cwd TEXT,name TEXT,title TEXT,source TEXT,updated_at INTEGER,archived INTEGER);"

		for _, row := range [][]string{{"latest", filepath.Join(codexAlias, ".worktrees/conflicted"), "Resolve\tcleanup conflicts", "Original long prompt", "cli", "100", "0"}, {"older", filepath.Join(conflicted, "src"), "Earlier conversation", "Old prompt", "vscode", "90", "1"}, {"helper", conflicted, "Review helper", "Helper prompt", `{"subagent":"review"}`, "200", "0"}, {"unrelated", conflicted + "-other", "Wrong conversation", "Other prompt", "cli", "300", "0"}} {
			values := make([]string, len(row))
			for index, value := range row {
				values[index] = sqlQuote(value)
			}

			sql += "INSERT INTO threads VALUES (" + strings.Join(values, ",") + ");"
		}

		result := f.execute(f.root, sqlite, database, sql)
		cleanupRequire(t, result.code == 0, "sqlite fixture: %s", result.stderr)

		databaseBefore = f.read(database)
	} else {
		t.Log("sqlite3 unavailable: Codex metadata assertions omitted; cleanup protection assertions still run")
	}

	trigger, ready := filepath.Join(f.root, "start-background"), filepath.Join(f.root, "background-ready")
	wrappers := filepath.Join(f.root, "wrappers")
	f.mkdir(wrappers)
	cleanupExecutable(f, filepath.Join(wrappers, "git"), `#!/bin/sh
if [ -n "$WORKTREE_TEST_CHECKED_PATH" ] && [ "$1" = '-C' ] && [ "$3" = 'ls-files' ] && [ "$4" = '--others' ] && [ "$5" = '--ignored' ]; then printf %s "$2" > "$WORKTREE_TEST_CHECKED_PATH"; fi
before= previous= current=
for arg do before=$previous; previous=$current; current=$arg; done
if [ "$before" = 'worktree' ] && [ "$previous" = 'remove' ] && [ ! -e "$WORKTREE_TEST_TRIGGER" ]; then
 : > "$WORKTREE_TEST_TRIGGER"
 count=0
 while [ ! -e "$WORKTREE_TEST_READY" ]; do count=$((count+1)); [ "$count" -lt 500 ] || exit 9; sleep .01; done
fi
exec "$WORKTREE_TEST_GIT" "$@"
`)
	baseEnv := cleanupCopyEnv(f.env)
	toolEnv := cleanupCopyEnv(baseEnv)
	toolEnv["PATH"] = wrappers + string(os.PathListSeparator) + baseEnv["PATH"]
	toolEnv["WORKTREE_TEST_GIT"] = f.gitBinary
	toolEnv["WORKTREE_TEST_TRIGGER"] = trigger
	toolEnv["WORKTREE_TEST_READY"] = ready
	toolEnv["WORKTREE_TEST_ACTIVE"] = active
	process := exec.CommandContext(t.Context(), "/bin/sh", "-c", `while [ ! -e "$WORKTREE_TEST_TRIGGER" ]; do sleep .01; done
cd "$WORKTREE_TEST_ACTIVE" || exit 9
: > "$WORKTREE_TEST_READY"
exec sleep 600`)
	process.Dir = f.root

	process.Env = cleanupEnv(toolEnv)
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if process.Process != nil {
			if err := process.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("stop owned background fixture: %v", err)
			}
		}

		if err := process.Wait(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != -1 {
				t.Errorf("wait for owned background fixture: %v", err)
			}
		}
	})

	run := func(cwd string, env map[string]string, args ...string) string {
		saved := f.env

		f.env = env
		defer func() { f.env = saved }()

		return cleanupRun(f, cwd, 0, args...)
	}

	script, err := exec.LookPath("script")
	if err != nil {
		t.Fatal("native script utility required for terminal acceptance: ", err)
	}

	previewEnv := cleanupCopyEnv(baseEnv)
	previewEnv["TERM"] = "xterm"
	delete(previewEnv, "NO_COLOR")

	terminalRun := func(name, command string) result {
		f.env = previewEnv
		defer func() { f.env = baseEnv }()

		logPath := filepath.Join(f.root, name+".log")
		terminalArgs := []string{script, "-q", logPath, "/bin/sh", "-c", command}

		if runtime.GOOS == "linux" {
			terminalArgs = []string{script, "-q", "-e", "-c", command, logPath}
		}

		return f.execute(root, terminalArgs...)
	}

	// rationale: Opposing redirections keep each tested stream on a PTY while making crossed output observable separately.
	tableDiagnostics := filepath.Join(f.root, "table-stderr")
	previewEnv["WORKTREE_TEST_STREAM_CAPTURE"] = tableDiagnostics
	terminal := terminalRun("table-terminal", `exec "$WORKTREE_BINARY" legacy-cleanup 2>"$WORKTREE_TEST_STREAM_CAPTURE"`)
	coloredPreview := terminal.stdout
	cleanupRequire(t, terminal.code == 0 && terminal.stderr == "", "table terminal status=%d: %s%s", terminal.code, terminal.stdout, terminal.stderr)
	cleanupContains(t, coloredPreview, "\x1b[32m       7\x1b[0m", "\x1b[31m       1\x1b[0m", "\x1b[31mMerge conflicts:\x1b[0m")
	cleanupRequire(t, f.read(tableDiagnostics) == "" && !strings.Contains(coloredPreview, "Discovering repositories"), "nonterminal progress or table diagnostics crossed streams: %s%s", coloredPreview, f.read(tableDiagnostics))

	plainTablePath := filepath.Join(f.root, "progress-stdout")
	previewEnv["WORKTREE_TEST_STREAM_CAPTURE"] = plainTablePath
	progressTerminal := terminalRun("progress-terminal", `exec "$WORKTREE_BINARY" legacy-cleanup >"$WORKTREE_TEST_STREAM_CAPTURE"`)
	progress := progressTerminal.stdout
	plainTable := f.read(plainTablePath)

	cleanupRequire(t, progressTerminal.code == 0 && progressTerminal.stderr == "", "progress terminal status=%d: %s%s", progressTerminal.code, progressTerminal.stdout, progressTerminal.stderr)
	cleanupContains(t, progress, "Discovering repositories", "Scanning repositories 2/2")
	cleanupContains(t, plainTable, "REPOSITORY", "DISK USAGE", "Repositories checked: 2")
	cleanupRequire(t, !strings.Contains(progress, "REPOSITORY") && !strings.Contains(progress, "Repositories checked:"), "table leaked to terminal stderr: %s", progress)
	cleanupRequire(t, !strings.Contains(plainTable, "Discovering repositories") && !strings.Contains(plainTable, "Scanning repositories") && !strings.Contains(plainTable, "\x1b["), "progress or color leaked to redirected stdout: %s", plainTable)

	streamsSeparated := func(table, diagnostics string) bool {
		return strings.Contains(table, "REPOSITORY") && !strings.Contains(table, "Discovering repositories") && strings.Contains(diagnostics, "Discovering repositories") && !strings.Contains(diagnostics, "REPOSITORY")
	}

	cleanupRequire(t, streamsSeparated(plainTable, progress), "cleanup streams are not separated")

	// rationale: A fixture-owned descriptor swap must contradict the same stream-boundary assertion.
	crossedTablePath := filepath.Join(f.root, "crossed-stdout")
	previewEnv["WORKTREE_TEST_STREAM_CAPTURE"] = crossedTablePath
	crossed := terminalRun("crossed-terminal", `exec 1>"$WORKTREE_TEST_STREAM_CAPTURE"; exec "$WORKTREE_BINARY" legacy-cleanup 3>&1 1>&2 2>&3`)
	cleanupRequire(t, crossed.code == 0 && crossed.stderr == "", "descriptor-swap fixture failed: %+v", crossed)
	cleanupContains(t, crossed.stdout, "REPOSITORY")
	cleanupRequire(t, !streamsSeparated(f.read(crossedTablePath), crossed.stdout), "descriptor swap escaped the stream-boundary assertion")

	zeroLine := cleanupLine(t, coloredPreview, "mode.txt")
	cleanupRequire(t, !regexp.MustCompile(`\x1b\[[0-9;]*m *0`).MatchString(zeroLine), "zero styled: %s", zeroLine)
	preview := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`).ReplaceAllString(coloredPreview, "")
	preview = strings.ReplaceAll(strings.ReplaceAll(preview, "\r\n", "\n"), "\r", "\n")
	cleanupContains(t, preview, "Repositories checked: 2", "REPOSITORY", "BASE", "CODEX SESSION", "4 worktree(s) would be removed", "Estimated reclaimable space", "reserved Treehouse pool", "ignored files", "hidden index flags", "Git operation in progress")
	cleanupRequire(t, strings.Count(preview, "DISK USAGE") == 1, "repeated report: %s", preview)

	if hasSQLite {
		cleanupContains(t, cleanupLine(t, preview, conflicted), "Resolve cleanup conflicts (+1 more)")
		cleanupRequire(t, !strings.Contains(preview, "Review helper") && !strings.Contains(preview, "Wrong conversation"), "wrong conversation: %s", preview)
		cleanupRequire(t, f.read(database) == databaseBefore, "Codex metadata modified")
	}

	cleanupRequire(t, !strings.Contains(preview, nested), "nested project discovered: %s", preview)
	cleanupRequire(t, f.exists(disposable) && f.exists(dependencies) && f.exists(completed), "preview deleted candidates")

	detailParts := strings.SplitN(preview, "Merge conflicts: "+conflicted+"\n", 2)
	cleanupRequire(t, len(detailParts) == 2, "missing conflict detail: %s", preview)
	details := detailParts[1]
	cleanupContains(t, details, "HEAD vs origin/main", "Showing 5 of 6 conflicting files; 1 more omitted", "HEAD +", "HEAD -", "BASE +", "BASE -", `a\tfile.txt`)
	cleanupRequire(t, !strings.Contains(details, "binary.dat"), "binary conflict not truncated: %s", details)
	fileParts := strings.SplitN(details, "FILE\n", 2)
	cleanupRequire(t, len(fileParts) == 2, "missing conflict file table: %s", details)
	fileLines := strings.Split(strings.SplitN(fileParts[1], "  Showing", 2)[0], "\n")
	cleanupRequire(t, strings.HasSuffix(fileLines[0], "largest file.txt") && reflect.DeepEqual(strings.Fields(fileLines[0])[:4], []string{"7", "1", "2", "1"}), "conflict ranking: %s", details)
	modeLine := cleanupLine(t, details, "mode.txt")
	cleanupRequire(t, reflect.DeepEqual(strings.Fields(modeLine)[:4], []string{"0", "0", "0", "1"}), "mode conflict: %s", details)
	cleanupRequire(t, f.gitAt(conflicted, "status", "--porcelain") == "" && f.gitAt(conflicted, "rev-parse", "HEAD") == conflictHead && f.read(f.gitAt(conflicted, "rev-parse", "--path-format=absolute", "--git-path", "index")) == conflictIndex, "conflict preview mutated checkout")
	header := cleanupHeader(t, preview)
	sizeAt, ageAt := strings.Index(header, "DISK USAGE"), strings.Index(header, "SIZE AGE")
	table := strings.SplitN(strings.SplitN(preview, header+"\n", 2)[1], "\n\n", 2)[0]
	sizes := []string{}
	numeric := []float64{}
	units := map[string]float64{"KiB": 1, "MiB": 1024, "GiB": 1024 * 1024, "TiB": 1024 * 1024 * 1024, "PiB": 1024 * 1024 * 1024 * 1024}

	for _, line := range strings.Split(table, "\n") {
		if line == "" {
			continue
		}

		cleanupRequire(t, len(line) >= ageAt, "short size row: %s", line)
		size := strings.TrimSpace(line[sizeAt:ageAt])

		sizes = append(sizes, size)
		if size != "-" {
			fields := strings.Fields(size)
			cleanupRequire(t, len(fields) == 2, "invalid size: %s", size)

			value, parseErr := strconv.ParseFloat(fields[0], 64)
			if parseErr != nil {
				t.Fatal(parseErr)
			}

			numeric = append(numeric, value*units[fields[1]])
		}
	}

	cleanupRequire(t, len(sizes) > 1 && sizes[len(sizes)-1] == "-" && len(numeric) == len(sizes)-1 && sort.Float64sAreSorted(numeric), "unsorted/unknown sizes: %v", sizes)
	cleanupRequire(t, strings.Contains(strings.Join(sizes, " "), "MiB") && strings.Contains(strings.Join(sizes, " "), "KiB"), "missing mixed sizes: %v", sizes)

	result := run(f.root, toolEnv, "-y", root)
	cleanupRequire(t, !strings.Contains(result, "\x1b["), "nonterminal color: %s", result)
	cleanupContains(t, result, "Removed 3 worktree(s)", "active process", "merge conflicts")
	cleanupRequire(t, !f.exists(disposable) && !f.exists(dependencies) && !f.exists(completed), "eligible candidates retained")

	for _, path := range []string{dirty, active, locked, pooled, nestedCheckout, operation, conflicted} {
		cleanupRequire(t, f.exists(path), "protected checkout removed: %s", path)
	}

	cleanupRequire(t, strings.Contains(f.gitAt(app, "worktree", "list", "--porcelain"), pooled), "pool unregistered")
	cleanupRequire(t, f.read(filepath.Join(dirty, ".gitignore")) == "unfinished edit\n" && f.read(filepath.Join(hidden, ".gitignore")) == "edit hidden from git status\n" && f.read(filepath.Join(evidence, "evidence/report.txt")) == "retain ignored evidence", "retained data changed")
	cleanupRequire(t, f.gitAt(app, "show-ref", "--verify", "refs/heads/completed-task") != "", "completed branch missing")
	cleanupRequire(t, f.gitAt(app, "status", "--porcelain") == "" && f.gitAt(library, "status", "--porcelain") == "", "primary checkout changed")
	add(library, dependencies, "--detach")
	f.gitAt(library, "worktree", "lock", "--reason", "Retain library discovery", dependencies)

	forceCheckout := filepath.Join(app, ".worktrees/discard-recent")
	add(app, forceCheckout, "-b", "force-unmerged")

	for _, name := range []string{"tracked.txt", "assumed.txt", "skipped.txt"} {
		text := "committed source\n"
		if name == "assumed.txt" {
			text = "committed assumed file\n"
		}

		if name == "skipped.txt" {
			text = "committed skipped file\n"
		}

		f.write(filepath.Join(forceCheckout, name), text)
	}

	f.gitAt(forceCheckout, "add", ".")

	recentEnv := cleanupCopyEnv(baseEnv)
	delete(recentEnv, "GIT_AUTHOR_DATE")
	delete(recentEnv, "GIT_COMMITTER_DATE")
	f.env = recentEnv
	f.gitAt(forceCheckout, "commit", "-qm", "Recent unmerged work")
	f.env = baseEnv
	forceHead := f.gitAt(forceCheckout, "rev-parse", "HEAD")
	f.gitAt(forceCheckout, "update-index", "--assume-unchanged", "assumed.txt")
	f.gitAt(forceCheckout, "update-index", "--skip-worktree", "skipped.txt")

	contents := map[string]string{"tracked.txt": "discard tracked edit\n", "assumed.txt": "discard hidden assumed edit\n", "skipped.txt": "discard hidden skipped edit\n", "untracked.txt": "discard untracked source\n", "evidence/report.txt": "discard ignored artifact\n"}

	f.mkdir(filepath.Join(forceCheckout, "evidence"))

	for name, text := range contents {
		f.write(filepath.Join(forceCheckout, name), text)
	}

	forceStatus := f.gitAt(forceCheckout, "status", "--porcelain")
	cleanupRequire(t, !strings.Contains(forceStatus, "assumed.txt") && !strings.Contains(forceStatus, "skipped.txt"), "hidden fixture visible: %s", forceStatus)

	detached := filepath.Join(app, ".worktrees/unmerged-detached")
	add(app, detached, "--detach")
	f.write(filepath.Join(detached, "detached.txt"), "retain detached work\n")
	f.gitAt(detached, "add", "detached.txt")
	f.gitAt(detached, "commit", "-qm", "Unmerged detached work")
	detachedHead := f.gitAt(detached, "rev-parse", "HEAD")
	mergeResult := f.execute(conflicted, f.gitBinary, "-c", "user.name=Cleanup check", "-c", "user.email=cleanup@example.invalid", "merge", "main")
	cleanupRequire(t, mergeResult.code == 1, "conflict merge status: %+v", mergeResult)
	cleanupRequire(t, f.gitAt(conflicted, "ls-files", "--unmerged") != "", "conflict merge had no unmerged entries")

	recheck := filepath.Join(app, ".worktrees/during-recheck")
	f.gitAt(app, "worktree", "add", "-b", "force-recheck-conflict", recheck, conflictHead)
	f.env = recentEnv
	f.gitAt(app, "commit", "--allow-empty", "-qm", "Recent merged base")
	f.env = baseEnv
	f.gitAt(app, "update-ref", "refs/remotes/origin/main", "HEAD")
	lateIgnored := filepath.Join(app, ".worktrees/late-ignored")
	add(app, lateIgnored, "--detach")
	lateHidden := filepath.Join(app, ".worktrees/late-hidden")
	f.gitAt(app, "worktree", "add", "--detach", lateHidden, "HEAD~1")
	protectedRegistration := f.gitAt(app, "worktree", "list", "--porcelain")
	forceIndexPath := f.gitAt(forceCheckout, "rev-parse", "--path-format=absolute", "--git-path", "index")
	forceIndexBefore := f.read(forceIndexPath)
	forcePreview := run(f.root, baseEnv, "--force", root)
	cleanupRequire(t, strings.HasPrefix(forcePreview, "Dry-run (pass -y to remove).\n\nREPOSITORY"), "force preface: %s", forcePreview)
	cleanupContains(t, forcePreview, "discard local files", "HEAD retained on branch", "unresolved conflicts", "active process")

	for path, status := range map[string]string{forceCheckout: "force-remove (dirty,ignored,unmerged,recent)", evidence: "force-remove (ignored)", lateIgnored: "force-remove (recent)", lateHidden: "force-remove", pooled: "skip"} {
		cleanupRequire(t, cleanupStatus(t, forcePreview, path) == status, "wrong status for %s: %s", path, forcePreview)
	}

	cleanupRequire(t, f.gitAt(app, "worktree", "list", "--porcelain") == protectedRegistration && f.read(forceIndexPath) == forceIndexBefore && f.gitAt(forceCheckout, "rev-parse", "HEAD") == forceHead && f.gitAt(forceCheckout, "status", "--porcelain") == forceStatus, "force preview mutated state")

	for name, text := range contents {
		cleanupRequire(t, f.read(filepath.Join(forceCheckout, name)) == text, "preview changed %s", name)
	}

	accepts := []string{"--accept-dirty", "--accept-ignored", "--accept-unmerged", "--accept-recent"}
	recentRetained := cleanupRows(t, cleanupObject(t, run(f.root, baseEnv, "--accept-dirty", "--accept-ignored", "--accept-unmerged", "--json", "--worktree", forceCheckout)))[forceCheckout]
	cleanupRequire(t, recentRetained["reason"] == "too new" && recentRetained["status"] == "skip (dirty,unmerged)", "recent acceptance: %+v", recentRetained)
	ignoredRetained := cleanupRows(t, cleanupObject(t, run(f.root, baseEnv, "--accept-dirty", "--accept-unmerged", "--accept-recent", "--json", "--worktree", forceCheckout)))[forceCheckout]
	cleanupRequire(t, ignoredRetained["reason"] == "ignored files" && ignoredRetained["status"] == "skip (dirty,unmerged,recent)", "ignored acceptance: %+v", ignoredRetained)
	singlePreview := cleanupObject(t, run(f.root, baseEnv, append(append([]string{}, accepts...), "--integrated", "--json", "--worktree", forceCheckout)...))
	singleRows := cleanupRows(t, singlePreview)
	single := singleRows[forceCheckout]
	expectedChecks := []any{"dirty", "ignored", "unmerged", "recent"}
	cleanupRequire(t, len(singleRows) == 1 && reflect.DeepEqual(singlePreview["accepted_checks"], expectedChecks) && single["action"] == "force-remove" && single["status"] == "force-remove (dirty,ignored,unmerged,recent)" && reflect.DeepEqual(single["accepted_conditions"], expectedChecks), "single acceptance: %+v", singlePreview)
	age, ok := single["head_age_seconds"].(float64)
	cleanupRequire(t, ok && age < 24*3600, "wrong recent age: %+v", single)

	incompatible := f.execute(f.root, f.binary, "legacy-cleanup", "--force", "--wip-push", "-y", root)
	cleanupRequire(t, incompatible.code == 1 && strings.Contains(incompatible.stderr, "--force excludes --wip-push"), "incompatible acceptance: %+v", incompatible)
	cleanupRequire(t, f.exists(forceCheckout) && f.gitAt(app, "for-each-ref", "refs/heads/wip/cleanup/") == "", "incompatible operation changed state")
	checkedPath := filepath.Join(f.root, "last-inspected-checkout")
	lateMerge := filepath.Join(f.root, "merge-during-activity")

	realLSOF, err := exec.LookPath("lsof")
	if err != nil {
		t.Fatal(err)
	}

	cleanupExecutable(f, filepath.Join(wrappers, "lsof"), `#!/bin/sh
if [ -f "$WORKTREE_TEST_CHECKED_PATH" ] && [ "$(cat "$WORKTREE_TEST_CHECKED_PATH")" = "$WORKTREE_TEST_RECHECK" ] && [ ! -e "$WORKTREE_TEST_ACTIVITY_MARKER" ]; then
 : > "$WORKTREE_TEST_ACTIVITY_MARKER"
 case "$WORKTREE_TEST_ACTIVITY_EVENT" in
 ignored) mkdir "$WORKTREE_TEST_RECHECK/evidence"; printf 'retain late ignored artifact\n' > "$WORKTREE_TEST_RECHECK/evidence/report.txt" ;;
 hidden) "$WORKTREE_TEST_GIT" -C "$WORKTREE_TEST_RECHECK" update-index --skip-worktree .gitignore || exit; printf 'retain late hidden edit\n' > "$WORKTREE_TEST_RECHECK/.gitignore" ;;
 merge) "$WORKTREE_TEST_GIT" -c user.name='Cleanup check' -c user.email=cleanup@example.invalid -C "$WORKTREE_TEST_RECHECK" merge main >/dev/null 2>&1; [ "$?" = 1 ] || exit 9 ;;
 *) exit 9 ;;
 esac
fi
exec "$WORKTREE_TEST_LSOF" "$@"
`)
	ignoredMarker := filepath.Join(f.root, "ignored-during-activity")

	ignoredEnv := cleanupCopyEnv(toolEnv)
	for key, value := range map[string]string{"WORKTREE_TEST_CHECKED_PATH": checkedPath, "WORKTREE_TEST_ACTIVITY_MARKER": ignoredMarker, "WORKTREE_TEST_ACTIVITY_EVENT": "ignored", "WORKTREE_TEST_RECHECK": lateIgnored, "WORKTREE_TEST_LSOF": realLSOF} {
		ignoredEnv[key] = value
	}

	ignoredRace := run(f.root, ignoredEnv, "--accept-recent", "-y", "--worktree", lateIgnored)
	cleanupRequire(t, f.exists(ignoredMarker), "ignored race did not run")
	cleanupContains(t, ignoredRace, "Removed 0 worktree(s)", "Retaining "+lateIgnored+": ignored files")
	cleanupRequire(t, f.read(filepath.Join(lateIgnored, "evidence/report.txt")) == "retain late ignored artifact\n" && strings.Contains(f.gitAt(app, "worktree", "list", "--porcelain"), lateIgnored), "late ignored data lost")
	hiddenMarker := filepath.Join(f.root, "hidden-edit-during-activity")
	hiddenEnv := cleanupCopyEnv(ignoredEnv)
	hiddenEnv["WORKTREE_TEST_ACTIVITY_MARKER"] = hiddenMarker
	hiddenEnv["WORKTREE_TEST_ACTIVITY_EVENT"] = "hidden"
	hiddenEnv["WORKTREE_TEST_RECHECK"] = lateHidden
	hiddenRace := run(f.root, hiddenEnv, "--accept-ignored", "-y", "--worktree", lateHidden)
	cleanupRequire(t, f.exists(hiddenMarker), "hidden race did not run")
	cleanupContains(t, hiddenRace, "Removed 0 worktree(s)", "Retaining "+lateHidden+": hidden index flags")
	cleanupRequire(t, f.read(filepath.Join(lateHidden, ".gitignore")) == "retain late hidden edit\n" && strings.Contains(f.gitAt(app, "worktree", "list", "--porcelain"), lateHidden), "late hidden data lost")
	dirtyResult := run(f.root, baseEnv, "--accept-dirty", "-y", root)
	cleanupContains(t, dirtyResult, "Removed 3 worktree(s)", "ignored files", "not merged")
	cleanupRequire(t, strings.HasPrefix(dirtyResult, "Applying cleanup.\n\nREPOSITORY") && cleanupStatus(t, dirtyResult, dirty) == "force-remove (dirty)" && cleanupStatus(t, dirtyResult, forceCheckout) == "skip (dirty)", "dirty acceptance: %s", dirtyResult)
	cleanupRequire(t, !f.exists(dirty) && !f.exists(hidden) && !f.exists(lateHidden) && f.exists(evidence) && f.exists(forceCheckout) && f.exists(detached), "dirty acceptance removed wrong candidates")
	cleanupRequire(t, f.read(filepath.Join(evidence, "evidence/report.txt")) == "retain ignored evidence", "ignored evidence changed")
	ignoredResult := run(f.root, baseEnv, "--accept-ignored", "-y", "--worktree", evidence)
	cleanupContains(t, ignoredResult, "Removed 1 worktree(s)")
	cleanupRequire(t, cleanupStatus(t, ignoredResult, evidence) == "remove (ignored)" && !f.exists(evidence) && f.exists(forceCheckout), "ignored acceptance: %s", ignoredResult)

	forceEnv := cleanupCopyEnv(ignoredEnv)
	forceEnv["WORKTREE_TEST_ACTIVITY_MARKER"] = lateMerge
	forceEnv["WORKTREE_TEST_ACTIVITY_EVENT"] = "merge"
	forceEnv["WORKTREE_TEST_RECHECK"] = recheck
	forceResult := run(f.root, forceEnv, append(append([]string{}, accepts...), "-y", root)...)
	cleanupContains(t, forceResult, "Removed 2 worktree(s)", "Retaining "+recheck+": unresolved conflicts")
	cleanupRequire(t, f.exists(lateMerge), "late merge did not run")
	appRegistration := f.gitAt(app, "worktree", "list", "--porcelain")

	libraryRegistration := f.gitAt(library, "worktree", "list", "--porcelain")
	for _, path := range []string{forceCheckout, dirty, hidden, evidence, lateIgnored, lateHidden} {
		cleanupRequire(t, !f.exists(path) && !strings.Contains(appRegistration, "worktree "+path+"\n"), "removed checkout registered/present: %s", path)
	}

	for _, path := range []string{app, pooled, locked, operation, conflicted, recheck, detached} {
		cleanupRequire(t, f.exists(path) && strings.Contains(appRegistration, "worktree "+path+"\n"), "protected checkout removed/unregistered: %s", path)
	}

	for _, path := range []string{active, dependencies} {
		cleanupRequire(t, f.exists(path) && strings.Contains(libraryRegistration, "worktree "+path+"\n"), "protected library checkout lost: %s", path)
	}

	cleanupRequire(t, f.gitAt(recheck, "ls-files", "--unmerged") != "" && f.exists(f.gitAt(recheck, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")), "late merge was not retained")
	cleanupRequire(t, f.gitAt(app, "rev-parse", "refs/heads/force-recheck-conflict") == conflictHead, "recheck branch changed")
	cleanupRequire(t, f.gitAt(detached, "rev-parse", "HEAD") == detachedHead && f.read(filepath.Join(detached, "detached.txt")) == "retain detached work\n", "detached work changed")
	cleanupRequire(t, f.gitAt(app, "rev-parse", "refs/heads/force-unmerged") == forceHead, "unmerged branch lost")

	for name, text := range map[string]string{"tracked.txt": "committed source", "assumed.txt": "committed assumed file", "skipped.txt": "committed skipped file"} {
		cleanupRequire(t, f.gitAt(app, "show", "force-unmerged:"+name) == text, "branch lost %s", name)
	}

	t.Logf("Cleanup parity retained protected work and exercised removal rechecks across %d source conflict names", len(conflictNames))
}
