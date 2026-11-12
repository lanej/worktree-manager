package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(os.Stderr)

	return f
}

func jsonFlag(args []string) ([]string, bool) {
	result := []string{}
	enabled := false

	for _, arg := range args {
		if arg == "--json" {
			enabled = true
		} else {
			result = append(result, arg)
		}
	}

	return result, enabled
}

func emit(command string, value any, machine bool) error {
	data, err := json.Marshal(exactNumbers(value))
	if err != nil {
		return err
	}

	decoded, err := decodeWire(data)
	if err != nil {
		return err
	}

	if machine {
		return writeJSON(map[string]any{"schema_version": 1, "command": command, "result": exactNumbers(decoded)})
	}

	return show(command, decoded)
}

func (p *policy) result(command string, value any, machine bool) error {
	err := emit(command, value, machine)
	if err == nil && machine {
		p.reported = true
	}

	return err
}

var errBudgetFull = errors.New("configured worktree budget is full")

func (p *policy) app(args []string) (int, error) {
	if len(args) == 0 || args[0] == "--help" || args[0] == "help" {
		_, err := fmt.Fprintln(os.Stdout, `worktree — ordinary Git worktrees, tracked and protected

Usage: worktree COMMAND [OPTIONS]

  create --branch NAME [--base HEAD] [--path PATH] [--repo PATH] [--json]
  finish TASK_ID|PATH [--json]        Finish protection; never delete
  backfill ROOT... [--json]          Discover existing repositories explicitly
  status [--json]                    Reconcile all known repositories and tasks
  prune [OPTIONS] [ROOT]             Preview; --apply/-y explicitly removes
  prune --all [OPTIONS]              Inspect all known repositories
  limits [--global] [--max-worktrees N|off] [--max-bytes SIZE|off]
  hooks [--file PATH]               Print or write a Git hook configuration fragment
  policy COMMAND                    Compatible git-worktree-policy interface

Pruning includes --worktree PATH, -a HOURS, -b BASE, --refresh, --min-size SIZE,
--integrated, --conflict-files N, --wip-push, --accept-dirty, --accept-ignored,
--accept-unmerged, --accept-recent and --force. Task protection, native locks,
active processes and unfinished Git operations cannot be overridden by --force.
Run worktree prune --help for the complete cleanup contract.

Installed runtime: Git; cleanup also needs Bash, lsof and standard Unix tools.
Codex session reporting uses optional sqlite3. No Python or Go installation needed.`)

		return 0, err
	}

	if args[0] == "--version" {
		_, err := fmt.Fprintln(os.Stdout, "worktree "+version)
		return 0, err
	}

	command, rest := args[0], args[1:]
	switch command {
	case "hooks":
		return p.hooksConfig(rest)
	case "policy":
		return p.run(rest)
	case "internal-cleanup-json":
		return 0, cleanupJSON(rest)
	case "internal-task-check":
		if len(rest) != 1 {
			return 2, errors.New("expected checkout path")
		}

		active, err := p.activeAt(rest[0])
		if err != nil {
			return 2, err
		}

		if active {
			return 1, nil
		}

		return 0, nil
	case "internal-url-allowed":
		if len(rest) != 1 {
			return 2, errors.New("expected URL")
		}

		allowed, err := recoveryAllowed(rest[0])
		if err != nil {
			return 2, err
		}

		if allowed {
			return 0, nil
		}

		return 1, nil
	case "legacy-cleanup":
		return p.cleanup(rest, false, false)
	case "prune":
		return p.prune(rest)
	case "create":
		return p.create(rest)
	case "finish":
		return p.finish(rest)
	case "backfill":
		return p.backfill(rest)
	case "status":
		statusArgs, machine := jsonFlag(rest)
		if len(statusArgs) > 0 {
			return 2, errors.New("status takes no arguments")
		}

		result, err := p.snapshot()
		if err != nil {
			return 1, err
		}

		status := 0
		if result["complete"] != true {
			status = 1
		}

		return status, p.result("status", result, machine)
	case "limits":
		return p.limits(rest)
	default:
		return 2, fmt.Errorf("unknown command: %s", command)
	}
}

func (p *policy) snapshot() (map[string]any, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	//nolint:gosec // rationale: Invoke this same trusted executable's compatibility interface, without a shell.
	child := exec.CommandContext(p.ctx, executable, "policy", "status", "--all")
	child.Stderr = os.Stderr
	output, err := child.Output()

	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		return nil, err
	}

	decoded, decodeErr := decodeWire(output)
	if decodeErr != nil {
		return nil, errors.Join(err, decodeErr)
	}

	report, valid := decoded.(map[string]any)
	if !valid {
		return nil, errors.New("invalid policy report")
	}

	tasks, err := loadTasks()
	if err != nil {
		return nil, err
	}

	taskRows, taskErr := p.taskReports(tasks)
	if taskErr != nil {
		return nil, taskErr
	}

	report["tasks"] = taskRows
	for _, row := range taskRows {
		if row["observation"] == "unknown" || row["observation"] == "identity_unverified" {
			report["complete"] = false
		}
	}

	return report, nil
}

func exactNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case map[string]any:
		for key, item := range typed {
			typed[key] = exactNumbers(item)
		}

		return typed
	case []any:
		for i, item := range typed {
			typed[i] = exactNumbers(item)
		}

		return typed
	case string:
		return gitText(typed)
	case nil, gitText, bool, float64:
		return value
	default:
		return value
	}
}

func (p *policy) limits(args []string) (int, error) {
	args, machine := jsonFlag(args)
	f := flags("limits")
	global := f.Bool("global", false, "global budgets")
	repo := f.String("repo", ".", "repository")
	count := f.String("max-worktrees", "", "count or off")

	size := f.String("max-bytes", "", "bytes or off")
	if err := f.Parse(args); err != nil {
		return 2, err
	}

	if f.NArg() != 0 {
		return 2, errors.New("unexpected limits arguments")
	}

	supplied := map[string]bool{}

	f.Visit(func(value *flag.Flag) { supplied[value.Name] = true })

	changes := []struct {
		local, global, value string
		number               *big.Int
	}{}
	for _, field := range []struct {
		name, local, global, value string
		bytes                      bool
	}{{"max-worktrees", "worktree.limit", "max_worktrees", *count, false}, {"max-bytes", "worktree.maxBytes", "max_bytes", *size, true}} {
		if !supplied[field.name] {
			continue
		}

		var (
			number *big.Int
			err    error
		)

		if field.value != "off" {
			if field.bytes {
				number, err = parseSize(field.value)
			} else {
				number, err = parseNumber(field.value)
			}
		}

		if err != nil {
			return 2, err
		}

		changes = append(changes, struct {
			local, global, value string
			number               *big.Int
		}{field.local, field.global, field.value, number})
	}

	if *global {
		if supplied["repo"] {
			return 2, errors.New("--global excludes --repo")
		}

		current, err := readLimits()
		if err != nil {
			return 1, err
		}

		for _, change := range changes {
			if change.number == nil {
				delete(current, change.global)
			} else {
				current[change.global] = change.number
			}
		}

		if len(changes) > 0 {
			root, dirErr := registryDir()
			if dirErr != nil {
				return 1, dirErr
			}

			if writeErr := atomicJSON(filepath.Join(filepath.Dir(root), "config.json"), current); writeErr != nil {
				return 1, writeErr
			}
		}

		return 0, p.result("limits", current, machine)
	}

	common, err := p.commonAt(*repo)
	if err != nil {
		return 1, err
	}

	for index, change := range changes {
		if change.number == nil {
			_, err = p.git(common, false, "config", "--local", "--unset-all", change.local)

			var exited *exec.ExitError
			if errors.As(err, &exited) && exited.ExitCode() == 5 {
				err = nil
			}
		} else {
			_, err = p.git(common, false, "config", "--local", change.local, change.value)
		}

		if err != nil {
			return 1, fmt.Errorf("setting %s failed after %d completed changes; inspect current limits: %w", change.local, index, err)
		}
	}

	countLimit, err := p.localLimit(common, "worktree.limit", false)
	if err != nil {
		return 1, err
	}

	byteLimit, err := p.localLimit(common, "worktree.maxBytes", true)
	if err != nil {
		return 1, err
	}

	return 0, p.result("limits", map[string]any{"repository": gitText(common), "max_worktrees": countLimit, "max_bytes": byteLimit}, machine)
}

func entry() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := &policy{ctx: ctx}

	status, err := p.app(os.Args[1:])
	if err != nil && !p.reported && len(os.Args) > 1 && contains(os.Args[2:], "--json") && os.Args[1] != "policy" && os.Args[1] != "legacy-cleanup" && !strings.HasPrefix(os.Args[1], "internal-") {
		code, outcome := "operation_failed", "unconfirmed"
		if status == 2 {
			code, outcome = "invalid_arguments", "rejected"
		}

		if errors.Is(err, errBudgetFull) {
			code, outcome = "budget_full", "blocked"
		}

		if printErr := writeJSON(map[string]any{"schema_version": 1, "command": os.Args[1], "error": map[string]string{"code": code, "outcome": outcome, "message": err.Error()}}); printErr != nil {
			return 1
		}
	}

	if err != nil {
		if _, printErr := fmt.Fprintln(os.Stderr, "worktree:", err); printErr != nil {
			return 1
		}

		if status == 0 {
			status = 1
		}
	}

	return status
}
func main() { os.Exit(entry()) }
