package main

import "os"

type callerEnvironment struct {
	agent    string
	owners   map[string]string
	detected bool
}

func callerFromEnvironment(getenv func(string) string) callerEnvironment {
	result := callerEnvironment{agent: "manual", owners: map[string]string{}}
	codexID := getenv("CODEX_THREAD_ID")

	if codexID == "" {
		codexID = getenv("CODEX_SESSION_ID")
	}

	for _, client := range []struct {
		agent, owner, marker, markerValue string
	}{
		{"codex", codexID, "", ""},
		{"claude", getenv("CLAUDE_CODE_SESSION_ID"), "CLAUDECODE", "1"},
		{"pi", getenv("PI_SESSION_ID"), "PI_CODING_AGENT", "true"},
		{"gemini", "", "GEMINI_CLI", "1"},
		{"opencode", getenv("OPENCODE_SESSION_ID"), "OPENCODE", "1"},
	} {
		if client.owner != "" || (client.marker != "" && getenv(client.marker) == client.markerValue) {
			result.owners[client.agent] = client.owner
		}
	}

	// rationale: AI_AGENT can contain an inherited client or a versioned value unsuitable as a slug.
	if len(result.owners) == 0 {
		if agent := getenv("AI_AGENT"); agent != "" {
			result.owners[agent] = ""
		}
	}

	result.detected = len(result.owners) != 0
	if result.detected {
		result.agent = ""

		if len(result.owners) == 1 {
			for agent := range result.owners {
				result.agent = agent
			}
		}
	}

	if agent := getenv("WORKTREE_AGENT"); agent != "" {
		result.agent = agent
		result.detected = true

		if owner := getenv("WORKTREE_OWNER_ID"); owner != "" {
			result.owners[agent] = owner
		}
	}

	return result
}

func currentCaller() callerEnvironment {
	return callerFromEnvironment(os.Getenv)
}
