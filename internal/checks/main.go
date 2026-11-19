package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

type projectPolicy struct {
	SchemaVersion int      `json:"schema_version"`
	PolicyVersion string   `json:"policy_version"`
	Stage         string   `json:"stage"`
	Languages     []string `json:"languages"`
}

type lifecycleRules struct {
	SchemaVersion int                              `json:"schema_version"`
	PolicyVersion string                           `json:"policy_version"`
	Stages        map[string]map[string][][]string `json:"stages"`
}

func commandPlan(policyData, rulesData []byte) (string, [][]string, error) {
	var policy projectPolicy
	if err := json.Unmarshal(policyData, &policy); err != nil {
		return "", nil, fmt.Errorf("project policy: %w", err)
	}

	var rules lifecycleRules
	if err := json.Unmarshal(rulesData, &rules); err != nil {
		return "", nil, fmt.Errorf("lifecycle rules: %w", err)
	}

	if policy.SchemaVersion != 1 || rules.SchemaVersion != 1 {
		return "", nil, errors.New("unsupported source-code schema version")
	}

	if policy.PolicyVersion != "1.0.0" || rules.PolicyVersion != policy.PolicyVersion {
		return "", nil, errors.New("unsupported or mismatched source-code policy version")
	}

	installed := false

	for _, language := range policy.Languages {
		switch language {
		case "go":
			installed = true
		case "typescript", "python":
		default:
			return "", nil, errors.New("invalid installed language list")
		}
	}

	if !installed {
		return "", nil, errors.New("language is not installed: go")
	}

	switch policy.Stage {
	case "research", "alpha", "beta", "production":
	default:
		return "", nil, fmt.Errorf("unsupported source-code stage: %s", policy.Stage)
	}

	commands := rules.Stages[policy.Stage]["go"]
	if len(commands) == 0 {
		return "", nil, errors.New("empty lifecycle command plan")
	}

	for _, command := range commands {
		if len(command) == 0 {
			return "", nil, errors.New("lifecycle commands must be nonempty argv arrays")
		}

		for _, argument := range command {
			if argument == "" {
				return "", nil, errors.New("lifecycle arguments must be nonempty strings")
			}
		}
	}

	return policy.Stage, commands, nil
}

func run(ctx context.Context, args []string) (int, error) {
	dryRun, language := false, false

	for _, argument := range args {
		switch argument {
		case "--dry-run":
			dryRun = true
		case "go":
			if language {
				return 2, errors.New("usage: scripts/check-source-code.sh go [--dry-run]")
			}

			language = true
		default:
			return 2, errors.New("usage: scripts/check-source-code.sh go [--dry-run]")
		}
	}

	if !language {
		return 2, errors.New("usage: scripts/check-source-code.sh go [--dry-run]")
	}

	policyData, err := os.ReadFile(".source-code.json")
	if err != nil {
		return 1, err
	}

	rulesData, err := os.ReadFile(".agents/skills/source-code-lifecycle/references/stages.json")
	if err != nil {
		return 1, err
	}

	stage, commands, err := commandPlan(policyData, rulesData)
	if err != nil {
		return 1, err
	}

	if _, err = fmt.Fprintln(os.Stdout, "Source-code go:", stage); err != nil {
		return 1, err
	}

	for _, argv := range commands {
		if dryRun {
			if err = json.NewEncoder(os.Stdout).Encode(argv); err != nil {
				return 1, err
			}

			continue
		}

		//nolint:gosec // rationale: Execute the validated repository-owned argv plan, preserving arguments without shell interpolation.
		command := exec.CommandContext(ctx, argv[0], argv[1:]...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr

		if err = command.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() > 0 {
				return exit.ExitCode(), err
			}

			return 1, err
		}
	}

	return 0, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	status, err := run(ctx, os.Args[1:])
	if err != nil {
		if _, printErr := fmt.Fprintln(os.Stderr, "Source-code:", err); printErr != nil {
			status = 1
		}
	}

	os.Exit(status)
}
