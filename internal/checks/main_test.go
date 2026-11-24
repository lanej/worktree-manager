package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandPlan(t *testing.T) {
	t.Parallel()

	const policy = `{"schema_version":1,"policy_version":"1.0.0","stage":"beta","languages":["go"]}`

	const rules = `{"schema_version":1,"policy_version":"1.0.0","stages":{"beta":{"go":[["bash","scripts/check-go.sh","all"]]}}}`

	for _, test := range []struct {
		name, policy, rules string
		valid               bool
	}{
		{"beta", policy, rules, true},
		{"current policy", strings.ReplaceAll(policy, "1.0.0", "1.1.0"), strings.ReplaceAll(rules, "1.0.0", "1.1.0"), true},
		{"mismatched policy", strings.ReplaceAll(policy, "1.0.0", "1.1.0"), rules, false},
		{"malformed policy", `{`, rules, false},
		{"malformed rules", policy, `{`, false},
		{"schema", `{"schema_version":2,"policy_version":"1.0.0","stage":"beta","languages":["go"]}`, rules, false},
		{"policy version", `{"schema_version":1,"policy_version":"9","stage":"beta","languages":["go"]}`, rules, false},
		{"stage", `{"schema_version":1,"policy_version":"1.0.0","stage":"unknown","languages":["go"]}`, rules, false},
		{"not installed", `{"schema_version":1,"policy_version":"1.0.0","stage":"beta","languages":[]}`, rules, false},
		{"invalid language", `{"schema_version":1,"policy_version":"1.0.0","stage":"beta","languages":["go","unknown"]}`, rules, false},
		{"missing commands", policy, `{"schema_version":1,"policy_version":"1.0.0","stages":{}}`, false},
		{"empty argv", policy, `{"schema_version":1,"policy_version":"1.0.0","stages":{"beta":{"go":[[]]}}}`, false},
		{"empty argument", policy, `{"schema_version":1,"policy_version":"1.0.0","stages":{"beta":{"go":[[""]]}}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			stage, commands, err := commandPlan([]byte(test.policy), []byte(test.rules))
			if (err == nil) != test.valid {
				t.Fatalf("unexpected plan validation: %v", err)
			}

			if test.valid && (stage != "beta" || !reflect.DeepEqual(commands, [][]string{{"bash", "scripts/check-go.sh", "all"}})) {
				t.Fatalf("unexpected command plan: %s %q", stage, commands)
			}
		})
	}
}

func TestLifecycleExecution(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	if err := os.WriteFile(".source-code.json", []byte(`{"schema_version":1,"policy_version":"1.1.0","stage":"beta","languages":["go"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	path := ".agents/skills/source-code-lifecycle/references/stages.json"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	data, err := json.Marshal(lifecycleRules{SchemaVersion: 1, PolicyVersion: "1.1.0", Stages: map[string]map[string][][]string{"beta": {"go": {{"/bin/sh", "-c", "exit 7"}}}}})
	if err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if status, runErr := run(t.Context(), []string{"go", "--dry-run"}); status != 0 || runErr != nil {
		t.Fatalf("dry run executed the failing command: %d %v", status, runErr)
	}

	if status, runErr := run(t.Context(), []string{"go"}); status != 7 || runErr == nil {
		t.Fatalf("lost child exit code: %d %v", status, runErr)
	}

	if status, runErr := run(t.Context(), []string{"go", "extra"}); status != 2 || runErr == nil {
		t.Fatalf("accepted unknown arguments: %d %v", status, runErr)
	}
}
