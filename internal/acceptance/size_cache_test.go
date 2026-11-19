//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func cleanupRequire(t *testing.T, condition bool, format string, values ...any) {
	t.Helper()

	if !condition {
		t.Fatalf(format, values...)
	}
}

func cleanupContains(t *testing.T, output string, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		cleanupRequire(t, strings.Contains(output, fragment), "missing %q in:\n%s", fragment, output)
	}
}

func cleanupRun(f *fixture, cwd string, status int, args ...string) string {
	f.t.Helper()
	result := f.execute(cwd, append([]string{f.binary, "legacy-cleanup"}, args...)...)
	cleanupRequire(f.t, result.code == status, "cleanup status=%d want=%d\n%s\n%s", result.code, status, result.stdout, result.stderr)

	return result.stdout
}

func cleanupLine(t *testing.T, output, ending string) string {
	t.Helper()

	for _, line := range strings.Split(output, "\n") {
		if strings.HasSuffix(strings.TrimSuffix(line, "\r"), ending) {
			return strings.TrimSuffix(line, "\r")
		}
	}

	t.Fatalf("no line ending %q in:\n%s", ending, output)

	return ""
}

func cleanupHeader(t *testing.T, output string) string {
	t.Helper()

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "REPOSITORY") {
			return line
		}
	}

	t.Fatalf("no report header:\n%s", output)

	return ""
}

func cleanupObject(t *testing.T, output string) map[string]any {
	t.Helper()

	var value map[string]any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		t.Fatal(err)
	}

	return value
}

func cleanupRows(t *testing.T, value map[string]any) map[string]map[string]any {
	t.Helper()

	rows, ok := value["worktrees"].([]any)
	cleanupRequire(t, ok, "invalid worktrees: %+v", value)

	result := map[string]map[string]any{}

	for _, item := range rows {
		row, valid := item.(map[string]any)
		cleanupRequire(t, valid, "invalid row: %+v", item)
		path, valid := row["path"].(string)
		cleanupRequire(t, valid, "invalid path: %+v", row)
		result[path] = row
	}

	return result
}

func cleanupPayload(t *testing.T, path string, length int) {
	t.Helper()

	if err := os.WriteFile(path, []byte(strings.Repeat("x", length)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func cleanupExecutable(f *fixture, path, source string) {
	f.t.Helper()
	f.write(path, source)
	//nolint:gosec // rationale: Only task-owned shell fixtures receive executable permission.
	if err := os.Chmod(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func TestSizeCache(t *testing.T) {
	f := newFixture(t)
	f.project(f.repo, "main")
	wrappers := filepath.Join(f.root, "wrappers")
	f.mkdir(wrappers)
	clock := filepath.Join(f.root, "clock")
	calls := filepath.Join(f.root, "du-calls")
	shrink := filepath.Join(f.root, "shrink-after-planning")
	unavailable := filepath.Join(f.root, "size-unavailable")

	const now = 946684800 + 51*3600
	f.write(clock, strconv.Itoa(now))

	for key, tool := range map[string]string{"WORKTREE_TEST_DU": "du", "WORKTREE_TEST_DATE": "date"} {
		actual, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}

		f.env[key] = actual
	}

	for key, value := range map[string]string{"PATH": wrappers + string(os.PathListSeparator) + f.env["PATH"], "WORKTREE_TEST_CALLS": calls, "WORKTREE_TEST_CLOCK": clock, "WORKTREE_TEST_SHRINK": shrink, "WORKTREE_TEST_DU_FAILURE": unavailable, "CODEX_HOME": filepath.Join(f.root, "codex")} {
		f.env[key] = value
	}

	cleanupExecutable(f, filepath.Join(wrappers, "du"), `#!/bin/sh
for last do :; done
printf '%s\n' "$last" >> "$WORKTREE_TEST_CALLS"
if [ -f "$WORKTREE_TEST_DU_FAILURE" ] && [ "$(cat "$WORKTREE_TEST_DU_FAILURE")" = "$last" ]; then exit 1; fi
if [ -f "$WORKTREE_TEST_SHRINK" ] && [ "$last" = "$WORKTREE_TEST_SIZE_CHECKOUT" ]; then
 "$WORKTREE_TEST_DU" "$@"
 status=$?
 if [ "$status" = 0 ]; then dd if=/dev/zero of="$WORKTREE_TEST_SIZE_PAYLOAD" bs=65536 count=1 2>/dev/null; rm "$WORKTREE_TEST_SHRINK"; fi
 exit "$status"
fi
exec "$WORKTREE_TEST_DU" "$@"
`)
	cleanupExecutable(f, filepath.Join(wrappers, "date"), `#!/bin/sh
if [ "$#" = 1 ] && [ "$1" = '+%s' ]; then cat "$WORKTREE_TEST_CLOCK"; else exec "$WORKTREE_TEST_DATE" "$@"; fi
`)
	checkout := filepath.Join(f.repo, ".worktrees/task")
	f.git("worktree", "add", "--detach", checkout)
	f.mkdir(filepath.Join(checkout, "node_modules"))
	payload := filepath.Join(checkout, "node_modules/payload")
	cleanupPayload(t, payload, 65536)

	scan := func(status int, args ...string) (string, string, string) {
		output := cleanupRun(f, f.repo, status, args...)
		header := cleanupHeader(t, output)
		line := cleanupLine(t, output, checkout)
		headAge, sizeAt, ageAt, codexAt := strings.Index(header, "AGE"), strings.Index(header, "DISK USAGE"), strings.Index(header, "SIZE AGE"), strings.Index(header, "CODEX SESSION")
		cleanupRequire(t, headAge >= 0 && sizeAt > headAge && ageAt > sizeAt && codexAt > ageAt && len(line) >= codexAt, "invalid cache report: %s", output)
		cleanupRequire(t, strings.TrimSpace(line[headAge:sizeAt]) == "2d 3h", "wrong HEAD age: %s", line)

		return line, strings.TrimSpace(line[sizeAt:ageAt]), strings.TrimSpace(line[ageAt:codexAt])
	}
	count := func(want int) {
		cleanupRequire(t, len(strings.Split(strings.TrimSpace(f.read(calls)), "\n")) == want, "wrong du calls, want %d: %s", want, f.read(calls))
	}
	cold, originalSize, age := scan(0)
	cleanupRequire(t, strings.Contains(cold, "remove") && age == "fresh", "cold report: %s", cold)
	count(1)
	f.write(filepath.Join(checkout, "unfinished.txt"), "preserve this edit")
	cleanupPayload(t, payload, 4*1024*1024)
	f.write(clock, strconv.Itoa(now+30))

	warm, size, age := scan(0)
	cleanupRequire(t, strings.Contains(warm, "dirty") && size == originalSize && age == "cached 30s", "warm report: %s", warm)
	count(1)

	refreshed, grownSize, age := scan(0, "--refresh")
	cleanupRequire(t, grownSize != originalSize && age == "fresh", "refresh: %s", refreshed)
	count(2)
	f.write(clock, strconv.Itoa(now+930))

	expired, size, age := scan(0)
	cleanupRequire(t, size == grownSize && age == "fresh", "expiry: %s", expired)
	count(3)

	previous := filepath.Join(f.repo, ".worktrees/previous")
	f.git("worktree", "move", checkout, previous)
	f.git("worktree", "add", "--detach", checkout)

	replaced, size, age := scan(0)
	cleanupRequire(t, size != grownSize && age == "fresh", "replacement: %s", replaced)
	count(5)
	cleanupRequire(t, f.read(filepath.Join(previous, "unfinished.txt")) == "preserve this edit", "previous edit changed")

	replacementSize := size
	resolvedCheckout := filepath.Join(f.repo, ".worktrees/real")

	if err := os.Rename(checkout, resolvedCheckout); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(resolvedCheckout, checkout); err != nil {
		t.Fatal(err)
	}

	alias, size, age := scan(1)
	cleanupRequire(t, strings.Contains(alias, "checkout check failed") && size == replacementSize && age == "fresh", "alias: %s", alias)
	f.git("worktree", "repair", resolvedCheckout)
	checkout = resolvedCheckout
	repaired, size, age := scan(0)
	cleanupRequire(t, size == replacementSize && age == "cached 0s", "repaired: %s", repaired)
	count(6)

	small := filepath.Join(f.repo, ".worktrees/small")
	f.git("worktree", "add", "--detach", small)
	f.mkdir(filepath.Join(checkout, "node_modules"))
	payload = filepath.Join(checkout, "node_modules/payload")
	cleanupPayload(t, payload, 4*1024*1024)

	registration := f.git("worktree", "list", "--porcelain")
	preview := cleanupObject(t, cleanupRun(f, f.repo, 0, "--min-size", "0.503808MB", "--json"))
	rows := cleanupRows(t, preview)
	cleanupRequire(t, preview["min_size_kib"] == float64(492), "threshold: %+v", preview)
	cleanupRequire(t, rows[checkout]["action"] == "remove" && rows[checkout]["size_age"] == "fresh", "large candidate: %+v", preview)
	kib, valid := rows[checkout]["size_kib"].(float64)
	cleanupRequire(t, valid && kib >= 1024, "large measurement: %+v", preview)
	cleanupRequire(t, rows[small]["action"] == "skip" && rows[small]["reason"] == "below min size (492.0 KiB)", "small candidate: %+v", preview)
	cleanupRequire(t, rows[previous]["reason"] == "dirty", "dirty candidate: %+v", preview)
	cleanupRequire(t, f.git("worktree", "list", "--porcelain") == registration, "preview changed registration")

	stat, err := os.Stat(payload)
	if err != nil {
		t.Fatal(err)
	}

	cleanupRequire(t, stat.Size() == 4*1024*1024, "preview changed payload")

	f.env["WORKTREE_TEST_SIZE_CHECKOUT"] = checkout
	f.env["WORKTREE_TEST_SIZE_PAYLOAD"] = payload
	f.write(shrink, "")
	rechecked := cleanupRun(f, f.repo, 0, "--min-size", fmt.Sprintf("%.0fKiB", kib), "-y")
	cleanupContains(t, cleanupLine(t, rechecked, checkout), "remove")

	stat, err = os.Stat(payload)
	if err != nil {
		t.Fatal(err)
	}

	cleanupRequire(t, !f.exists(shrink) && stat.Size() == 65536, "shrink fixture did not run")
	cleanupContains(t, rechecked, "Retaining "+checkout+": below min size", "Removed 0 worktree(s)")
	cleanupRequire(t, f.exists(checkout) && f.exists(small) && f.exists(previous), "shrinking checkouts removed")
	f.write(unavailable, small)
	failed := cleanupObject(t, cleanupRun(f, f.repo, 1, "--min-size", "1MiB", "--json"))
	failedRows := cleanupRows(t, failed)
	cleanupRequire(t, failed["errors"] == float64(1) && failedRows[small]["action"] == "skip" && failedRows[small]["reason"] == "size unavailable" && failedRows[small]["size_kib"] == nil, "unavailable size: %+v", failed)
	cleanupRequire(t, f.exists(small) && f.exists(checkout), "unknown sizes were removed")

	if err := os.Remove(unavailable); err != nil {
		t.Fatal(err)
	}

	cleanupPayload(t, payload, 1536*1024)

	removed := cleanupRun(f, f.repo, 0, "--min-size", "1.5MiB", "-y")
	cleanupContains(t, removed, "Removed 1 worktree(s)")
	cleanupRequire(t, !f.exists(checkout) && !strings.Contains(f.git("worktree", "list", "--porcelain"), checkout), "large checkout retained")
	cleanupRequire(t, f.exists(small) && strings.Contains(f.git("worktree", "list", "--porcelain"), small), "small checkout removed")
	cleanupRequire(t, f.exists(previous) && f.read(filepath.Join(previous, "unfinished.txt")) == "preserve this edit", "dirty checkout changed")
}
