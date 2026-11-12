package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (p *policy) hooksConfig(args []string) (status int, resultErr error) {
	f := flags("hooks")

	file := f.String("file", "", "write a separate Git configuration fragment")
	if err := f.Parse(args); err != nil {
		return 2, err
	}

	if f.NArg() != 0 {
		return 2, errors.New("hooks takes only --file PATH")
	}

	executable, err := os.Executable()
	if err != nil {
		return 1, err
	}

	printing := *file == ""
	if printing {
		temporary, createErr := os.CreateTemp("", "worktree-hooks-")
		if createErr != nil {
			return 1, createErr
		}

		*file = temporary.Name()
		if closeErr := temporary.Close(); closeErr != nil {
			return 1, errors.Join(closeErr, os.Remove(*file))
		}

		defer func() { resultErr = errors.Join(resultErr, os.Remove(*file)) }()
	}

	path, err := filepath.Abs(*file)
	if err != nil {
		return 1, err
	}

	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 1, err
	}

	for _, hook := range []struct{ name, event string }{{"pre", "pre-worktree"}, {"post", "post-worktree"}, {"checkout", "post-checkout"}} {
		for _, entry := range []struct{ key, value string }{{"event", hook.event}, {"command", shellQuote(executable) + " policy observe " + hook.event}, {"enabled", "true"}} {
			if _, err = p.command("", "config", "--file", path, "hook.worktree-manager-"+hook.name+"."+entry.key, entry.value); err != nil {
				return 1, err
			}
		}
	}

	if printing {
		//nolint:gosec // rationale: Print the private fragment serialized by Git itself, preserving Git-config and shell quoting separately.
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return 1, readErr
		}

		_, err = os.Stdout.Write(data)
	} else {
		_, err = fmt.Fprintln(os.Stdout, "Wrote hook configuration:", path, "\nInclude this file from your Git configuration. Existing hook files and core.hooksPath were preserved.")
	}

	return 0, err
}
