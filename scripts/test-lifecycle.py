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
    env.update(HOME=str(home), GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM="1",
               XDG_STATE_HOME=str(home / "state[1]"), XDG_CACHE_HOME=str(home / "cache"),
               GIT_AUTHOR_DATE="2000-01-01T00:00:00Z", GIT_COMMITTER_DATE="2000-01-01T00:00:00Z")
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
    created = call("create", "--branch", "task/first")
    path = Path(created["path"])
    assert created["state"] == "active" and path.is_dir()
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
    hook.write_text(prefix + 'git -C ' + shlex.quote(str(repo)) + ' worktree remove --force --force ' + shlex.quote(str(vanished)) + '\n')
    missing = call("create", "--branch", "vanished-task", "--path", vanished, ok=False)
    assert missing["state"] == "uncertain" and not vanished.exists()
    call("finish", missing["id"])
    (repo / ".git/hooks/post-checkout").write_text("#!/bin/sh\nexit 7\n")
    (repo / ".git/hooks/post-checkout").chmod(0o755)
    partial = call("create", "--branch", "hook-failure", ok=False)
    assert partial["state"] == "active" and Path(partial["path"]).exists() and "exit status 7" in partial["error"]
    print("Lifecycle: protected create, finish, safe pruning, limits, backfill and partial Git outcomes passed")
