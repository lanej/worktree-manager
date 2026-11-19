<!-- Project-owned native verification preserves the installed beta/tool policy. -->
# Source-code project guidance

Policy version: **1.0.0**. Schema version: **1**. Lifecycle stage: **beta**.

Project kind: **tool**. Read the installed `.agents/skills/source-code-lifecycle/references/kinds.md` for kind-specific obligations, scaled by the selected lifecycle stage. Kind selects usage guidance, not architecture or additional generated test commands.

Before applying this policy, read the repository's `AGENTS.md` if it exists. Preserve its requirements and all more-specific repository instructions. This override adds project policy; it does not waive existing gates.

The authoritative pins and stage are in `.source-code.json`; this file summarizes the project-owned native profile.

| Skill | Version |
| --- | --- |
| architecture | 1.0.0 |
| builder | 1.0.1 |
| code-comments | 1.0.0 |
| data-change-planning | 1.0.0 |
| git | 1.0.0 |
| go | 1.0.1 |
| library-code-comments | 1.0.0 |
| pr-writer | 1.0.0 |
| source-code-lifecycle | 1.0.0 |
| testing | 1.0.0 |

Read the installed `.agents/skills/source-code-lifecycle/SKILL.md` first, then load the installed skills relevant to the task. Preserve existing repository instructions and required gates. A stage or version pin does not establish readiness.

Use `source-code validate` to check packages and pins. Use `source-code upgrade --dry-run` to review available updates before applying them. Run the native project gate: `scripts/check-source-code.sh go`.

This file is the Codex project entrypoint. Other hosts should load it explicitly when their native instruction discovery does not read AGENTS.override.md.
