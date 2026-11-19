#!/usr/bin/env python3
"""Exercise the native entrypoint with disposable, real Git repositories."""
import json
import os
import shlex
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile

binary = str(Path(sys.argv[1]).resolve())
with tempfile.TemporaryDirectory(prefix="worktree-lifecycle-") as temporary:
    root = Path(temporary).resolve()
    home = root / "home with 'quotes"
    home.mkdir()
    repo = root / "repo"
    repo.mkdir()
    env = {k: v for k, v in os.environ.items() if not k.startswith(("GIT_", "XDG_", "WORKTREE_"))}
    for variable in ("CODEX_THREAD_ID", "CODEX_SESSION_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE",
                     "PI_SESSION_ID", "PI_CODING_AGENT", "GEMINI_CLI", "OPENCODE_SESSION_ID", "OPENCODE", "AI_AGENT"):
        env.pop(variable, None)
    env.update(HOME=str(home), GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
               XDG_STATE_HOME=str(home / "state[1]"), XDG_CACHE_HOME=str(home / "cache"),
               GIT_AUTHOR_DATE="2000-01-01T00:00:00Z", GIT_COMMITTER_DATE="2000-01-01T00:00:00Z")
    if sys.platform == "darwin":
        env["LC_ALL"] = "en_US.UTF-8"
    def run(*args, ok=True, cwd=repo):
        result = subprocess.run([str(a) for a in args], cwd=cwd, env=env, capture_output=True, text=True, timeout=180)
        assert (result.returncode == 0) == ok, (args, result.returncode, result.stdout, result.stderr)
        return result
    def git(*args, **kw):
        return run("git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", *args, **kw)
    def call(*args, **kw):
        return json.loads(run(binary, *args, "--json", **kw).stdout)["result"]
    git("init", "-q", "--initial-branch=main")
    (repo / ".gitignore").write_text(".worktrees/\nnode_modules/\n")
    (repo / "tracked").write_text("preserve branches\n")
    git("add", ".")
    git("commit", "-qm", "initial")
    before = git("for-each-ref", "--format=%(refname)").stdout
    for slug in ("../escape", "Codex", "", "a" * 49):
        failure = run(binary, "create", "--agent", slug, "--json", ok=False)
        assert json.loads(failure.stdout)["error"]["code"] == "invalid_arguments"
    (repo / ".gitignore").write_text("node_modules/\n")
    assert "must be ignored" in run(binary, "create", "--agent", "codex", ok=False).stderr
    (repo / ".gitignore").write_text(".worktrees/\nnode_modules/\n")
    outside = root / "outside"
    outside.mkdir()
    (repo / ".worktrees").symlink_to(outside, target_is_directory=True)
    assert "symlink" in run(binary, "create", "--agent", "codex", ok=False).stderr
    assert not list(outside.iterdir())
    (repo / ".worktrees").unlink()
    assert git("for-each-ref", "--format=%(refname)").stdout == before
    records = home / "state[1]/worktree-manager/tasks"
    assert not records.exists()
    git("branch", "worktree")
    assert "namespace conflicts" in run(binary, "create", "--agent", "codex", ok=False).stderr
    assert not records.exists()
    git("branch", "-d", "worktree")
    manual = call("create")
    assert manual["agent"] == "manual" and "created_at" in manual and "gitdir" in manual
    call("finish", manual["id"])
    git("worktree", "remove", manual["path"])
    for identity_environment, expected_agent, expected_owner in (
        ({"CODEX_THREAD_ID": "calling-agent-id"}, "codex", "calling-agent-id"),
        ({"CODEX_SESSION_ID": "calling-agent-id"}, "codex", "calling-agent-id"),
        ({"CLAUDE_CODE_SESSION_ID": "calling-agent-id", "CLAUDECODE": "1"}, "claude", "calling-agent-id"),
        ({"CLAUDECODE": "1"}, "claude", ""),
        ({"PI_SESSION_ID": "calling-agent-id", "PI_CODING_AGENT": "true", "AI_AGENT": "pi"}, "pi", "calling-agent-id"),
        ({"PI_CODING_AGENT": "true"}, "pi", ""),
        ({"GEMINI_CLI": "1"}, "gemini", ""),
        ({"OPENCODE_SESSION_ID": "calling-agent-id", "OPENCODE": "1", "AI_AGENT": "opencode"}, "opencode", "calling-agent-id"),
        ({"OPENCODE": "1"}, "opencode", ""),
        ({"WORKTREE_AGENT": "custom", "WORKTREE_OWNER_ID": "calling-agent-id"}, "custom", "calling-agent-id"),
        ({"AI_AGENT": "custom"}, "custom", ""),
    ):
        env.update(identity_environment)
        try:
            for output_flags in ((), ("--json",), ("--verbose",)):
                response = json.loads(run(binary, "create", *output_flags).stdout)
                assert set(response) == {"command", "result", "schema_version"}
                assert response["command"] == "create" and response["schema_version"] == 1
                agent_task = response["result"]
                assert agent_task["agent"] == expected_agent and agent_task.get("owner_id", "") == expected_owner
                compact_keys = {"id", "path", "branch", "state", "agent"} | ({"owner_id"} if expected_owner else set())
                if "--verbose" in output_flags:
                    assert {"agent", "common_gitdir", "gitdir", "created_at"} <= set(agent_task)
                else:
                    assert set(agent_task) == compact_keys
                assert Path(agent_task["path"]).exists() and agent_task["state"] == "active"
                for _ in range(2):
                    finished_agent = json.loads(run(binary, "finish", agent_task["id"]).stdout)["result"]
                    assert set(finished_agent) == compact_keys
                    assert finished_agent["id"] == agent_task["id"] and finished_agent["state"] == "finished"
                verbose_finish = json.loads(run(binary, "finish", agent_task["id"], "--verbose").stdout)["result"]
                assert "finished_at" in verbose_finish and "gitdir" in verbose_finish
                git("worktree", "remove", agent_task["path"])
            for selected_agent, selected_owner in (("pi", "launcher-owner"), ("codex", "")):
                explicit_task = call("create", "--agent", selected_agent, "--owner-id", selected_owner)
                assert explicit_task["agent"] == selected_agent
                assert explicit_task.get("owner_id", "") == selected_owner
                assert ("owner_id" in explicit_task) == bool(selected_owner)
                call("finish", explicit_task["id"])
                git("worktree", "remove", explicit_task["path"])
            rejected_agent = json.loads(run(binary, "create", "--agent", "../escape", ok=False).stdout)
            assert rejected_agent["error"]["code"] == "invalid_arguments"
            for positional_args in (("create", "--", "--verbose"), ("finish", "--", "--verbose")):
                rejected_positional = json.loads(run(binary, *positional_args, ok=False).stdout)
                assert rejected_positional["error"]["code"] == "invalid_arguments"
            opaque_owner = call("create", "--owner-id", "--verbose")
            assert opaque_owner["owner_id"] == "--verbose" and "gitdir" not in opaque_owner
            call("finish", opaque_owner["id"])
            git("worktree", "remove", opaque_owner["path"])
            rejected_finish = json.loads(run(binary, "finish", ok=False).stdout)
            assert rejected_finish["error"]["code"] == "invalid_arguments"
        finally:
            for variable in identity_environment:
                env.pop(variable)
    env.update(CODEX_THREAD_ID="codex-owner", CLAUDE_CODE_SESSION_ID="claude-owner")
    try:
        before_conflict = git("for-each-ref", "--format=%(refname)").stdout
        rejected_conflict = json.loads(run(binary, "create", ok=False).stdout)
        assert rejected_conflict["error"]["code"] == "invalid_arguments"
        assert "multiple agent environments" in rejected_conflict["error"]["message"]
        assert git("for-each-ref", "--format=%(refname)").stdout == before_conflict
        for selector in ("flag", "environment"):
            if selector == "environment":
                env["WORKTREE_AGENT"] = "claude"
            selected = call("create", *( ("--agent", "claude") if selector == "flag" else () ))
            assert selected["agent"] == "claude" and selected["owner_id"] == "claude-owner"
            call("finish", selected["id"])
            git("worktree", "remove", selected["path"])
        env["WORKTREE_AGENT"] = "../escape"
        invalid_environment = json.loads(run(binary, "create", ok=False).stdout)
        assert invalid_environment["error"]["code"] == "invalid_arguments"
    finally:
        for variable in ("CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "WORKTREE_AGENT"):
            env.pop(variable, None)
    created = call("create", "--agent", "codex", "--owner-id", "full-launch-token")
    path = Path(created["path"])
    assert created["state"] == "active" and path.is_dir()
    assert path == repo / ".worktrees" / ("codex-" + created["id"][:8])
    assert created["agent"] == "codex" and created["owner_id"] == "full-launch-token"
    assert created["branch"] == "worktree/" + path.name
    assert len(created["id"]) == 32
    human = run(binary, "status").stdout
    assert "full-launch-token" in human and created["branch"] in human
    linked = call("create", "--agent", "claude", cwd=path)
    assert Path(linked["path"]).parent == repo / ".worktrees"
    assert linked["branch"].startswith("worktree/claude-")
    call("finish", linked["id"])
    git("worktree", "remove", linked["path"])
    legacy = json.loads((records / (linked["id"] + ".json")).read_text())
    del legacy["agent"]
    (records / (linked["id"] + ".json")).write_text(json.dumps(legacy))
    assert next(t for t in call("status")["tasks"] if t["id"] == linked["id"])["state"] == "finished"
    git("branch", "-m", "task/first", cwd=path)
    observed = next(t for t in call("status")["tasks"] if t["id"] == created["id"])
    assert observed["observed_branch"] == "task/first" and observed["observed_path"] == str(path)
    git("checkout", "--detach", cwd=path)
    detached = next(t for t in call("status")["tasks"] if t["id"] == created["id"])
    assert detached["observation"] == "available" and detached["observed_branch"] is None
    display = next(line for line in run(binary, "status").stdout.splitlines() if line.startswith(created["id"]))
    assert created["branch"] not in display
    git("checkout", "task/first", cwd=path)
    before = git("for-each-ref", "--format=%(refname)").stdout
    rejected = run(binary, "create", "--branch", "task/first", "--json", ok=False)
    assert "already exists" in rejected.stderr and rejected.returncode == 2
    assert json.loads(rejected.stdout)["error"] == {
        "code": "invalid_arguments", "outcome": "rejected",
        "message": "allocation rejected: branch already exists; create requires a fresh named branch"}
    assert "administrative" in run(binary, "create", "--path", repo / ".git/not-a-checkout", ok=False).stderr
    assert git("for-each-ref", "--format=%(refname)").stdout == before
    native_path = root / "native-tools"
    native_path.mkdir()
    for name in ("awk", "basename", "cat", "column", "cut", "date", "dirname", "du", "find", "git", "head", "id", "lsof", "ls", "mkdir", "mktemp", "mv", "od", "printf", "pwd", "readlink", "rm", "sed", "sleep", "sort", "stat", "tail", "tr", "uname", "wc", "xargs"):
        tool = shutil.which(name)
        if tool:
            (native_path / name).symlink_to(tool)
    assert shutil.which("python3", path=str(native_path)) is None
    assert shutil.which("go", path=str(native_path)) is None
    previous_path = env["PATH"]
    env["PATH"] = str(native_path)
    try:
        native_report = call("prune", "--all")
        assert next(r for r in native_report["worktrees"] if r["path"] == str(path))["reason"] == "active task"
    finally:
        env["PATH"] = previous_path

    assert call("status")["totals"]["linked_count"] == "1"
    report = call("prune", "--all", "--force")
    assert next(r for r in report["worktrees"] if r["path"] == str(path))["reason"] == "active task"
    git("worktree", "unlock", path)
    report = call("prune", "--all", "--force")
    assert next(r for r in report["worktrees"] if r["path"] == str(path))["reason"] == "active task"
    git("worktree", "lock", "--reason", "external owner", path)
    finished = call("finish", created["id"])
    assert (Path(created["gitdir"]) / "locked").read_text().strip() == "external owner"
    git("worktree", "unlock", path)
    assert finished["state"] == "finished" and path.exists()
    assert call("finish", created["id"])["state"] == "finished"
    (path / "unfinished").write_text("keep me")
    report = call("prune", "--all")
    assert next(r for r in report["worktrees"] if r["path"] == str(path))["reason"] == "dirty", report
    (path / "unfinished").unlink()
    report = call("prune", "--all", "--apply")
    assert next(r for r in report["worktrees"] if r["path"] == str(path))["outcome"] == "removed"
    assert not path.exists()
    git("show-ref", "--verify", "refs/heads/task/first")
    git("worktree", "add", "-b", "replacement-registration", path)
    historical = next(t for t in call("status")["tasks"] if t["id"] == created["id"])
    assert historical["observation"] != "available" and historical["observed_path"] is None
    git("worktree", "remove", path)

    call("limits", "--max-worktrees", "0")
    denied = run(binary, "create", "--branch", "blocked", "--json", ok=False)
    assert json.loads(denied.stdout)["error"]["code"] == "budget_full"
    assert "full" in denied.stderr
    git("show-ref", "--verify", "refs/heads/blocked", ok=False)
    call("limits", "--max-worktrees", "off")
    backfill = call("backfill", root)
    assert backfill["complete"] and len(backfill["repositories"]) == 1
    assert call("prune", repo)["outcome"] == "preview"
    forced = root / "force-receipt"
    git("worktree", "add", "-b", "force-receipt", forced)
    (forced / "dirty").write_text("explicitly discard this fixture")
    report = call("prune", "--force", "--apply", repo)
    assert next(r for r in report["worktrees"] if r["path"] == str(forced))["outcome"] == "removed"
    assert not forced.exists()
    changing = root / "eligibility-changes"
    git("worktree", "add", "-b", "eligibility-changes", changing)
    dirty = changing / "dirty"
    dirty.write_text("becomes clean after preview")
    wrappers = root / "wrappers"
    wrappers.mkdir()
    counter = root / "lsof-counter"
    probe = wrappers / "lsof"
    probe.write_text('#!/bin/sh\nn=0\n[ ! -f ' + shlex.quote(str(counter)) + ' ] || n=$(cat ' + shlex.quote(str(counter)) + ')\nn=$((n+1))\nprintf "%s\\n" "$n" > ' + shlex.quote(str(counter)) + '\nif [ "$n" -eq 2 ]; then rm -f ' + shlex.quote(str(dirty)) + '; fi\nexit 0\n')
    probe.chmod(0o755)
    previous_path = env["PATH"]
    env["PATH"] = str(wrappers) + os.pathsep + previous_path
    try:
        report = call("prune", repo, "--apply")
        row = next(r for r in report["worktrees"] if r["path"] == str(changing))
        assert row["outcome"] == "not_attempted" and changing.exists() and not dirty.exists()
    finally:
        env["PATH"] = previous_path
    git("worktree", "remove", changing)
    # Protected task checkouts remain safe when an ancestor checkout is pruned.
    for moved_other_repo in (False, True):
        outer = root / ("outer-moved" if moved_other_repo else "outer")
        git("worktree", "add", "-b", outer.name, outer)
        source = repo
        if moved_other_repo:
            source = root / "other-repository"
            source.mkdir()
            git("init", "-q", "--initial-branch=main", cwd=source)
            git("commit", "--allow-empty", "-qm", "initial", cwd=source)
        initial = root / "before-nesting" if moved_other_repo else outer / "nested"
        nested_task = call("create", "--branch", "task/" + outer.name, "--path", initial, cwd=source)
        nested = outer / "nested"
        if moved_other_repo:
            git("worktree", "move", "--force", "--force", initial, nested, cwd=source)
        valuable = nested / "uncommitted"
        valuable.write_text("retain this active task's work\n")
        previous_path = env["PATH"]
        probe.write_text("#!/bin/sh\nexit 0\n")
        env["PATH"] = str(wrappers) + os.pathsep + previous_path
        try:
            report = call("prune", "--worktree", outer, "--force", "--apply")
            row = next(r for r in report["worktrees"] if r["path"] == str(outer))
            assert row["reason"] == "active task" and row["outcome"] == "not_attempted", row
            assert valuable.read_text() == "retain this active task's work\n"
        finally:
            env["PATH"] = previous_path
        call("finish", nested_task["id"])
        valuable.unlink()
        git("worktree", "remove", nested, cwd=source)
        git("worktree", "remove", outer)
    call("limits", "--max-worktrees", "1")
    run(binary, "limits", "--max-worktrees", "off", "--max-bytes", "invalid", ok=False)
    assert git("config", "--local", "--get", "worktree.limit").stdout.strip() == "1"
    call("limits", "--max-worktrees", "off", "--max-bytes", "off")
    assert call("limits", "--global", "--max-worktrees", "9007199254740993")["max_worktrees"] == "9007199254740993"
    call("limits", "--global", "--max-worktrees", "off")
    before_move, after_move = root / "before-move", root / "after-move"
    hook = repo / ".git/hooks/post-checkout"
    prefix = '#!/bin/sh\nunset GIT_DIR GIT_COMMON_DIR GIT_WORK_TREE GIT_INDEX_FILE\n'
    hook.write_text(prefix + 'git -C ' + shlex.quote(str(repo)) + ' worktree move --force --force ' + shlex.quote(str(before_move)) + ' ' + shlex.quote(str(after_move)) + '\n')
    hook.chmod(0o755)
    moved = call("create", "--branch", "moved-task", "--path", before_move)
    assert moved["state"] == "active" and moved["path"] == str(after_move)
    second_move = root / "second-move"
    git("worktree", "move", "--force", "--force", after_move, second_move)
    moved_report = next(t for t in call("status")["tasks"] if t["id"] == moved["id"])
    assert moved_report["observed_path"] == str(second_move)
    call("finish", second_move)
    git("worktree", "remove", second_move)
    vanished = root / "vanished"
    env["CODEX_THREAD_ID"] = "calling-agent-id"
    hook.write_text(prefix + 'git -C ' + shlex.quote(str(repo)) + ' worktree remove --force --force ' + shlex.quote(str(vanished)) + '\n')
    missing = call("create", "--branch", "vanished-task", "--path", vanished, ok=False)
    assert set(missing) == {"id", "path", "branch", "state", "agent", "owner_id", "error"}
    assert missing["state"] == "uncertain" and not vanished.exists()
    call("finish", missing["id"])
    (repo / ".git/hooks/post-checkout").write_text("#!/bin/sh\nexit 7\n")
    (repo / ".git/hooks/post-checkout").chmod(0o755)
    partial = call("create", "--branch", "hook-failure", ok=False)
    assert set(partial) == {"id", "path", "branch", "state", "agent", "owner_id", "error"}
    assert partial["state"] == "active" and Path(partial["path"]).exists() and "exit status 7" in partial["error"]
    print("Lifecycle: protected create, finish, safe pruning, limits, backfill and partial Git outcomes passed")
