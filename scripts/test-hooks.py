#!/usr/bin/env python3
"""Exercise actual configured hooks with the published patched Git runtime."""
import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile

binary, git = map(lambda p: str(Path(p).resolve()), sys.argv[1:3])
with tempfile.TemporaryDirectory(prefix='worktree-hooks-') as temporary:
    root = Path(temporary).resolve()
    home = root / "home with 'quotes"
    home.mkdir()
    installed = home / "bin # quote's\\backslash/work tree's"
    installed.parent.mkdir()
    shutil.copyfile(binary, installed)
    installed.chmod(0o755)
    binary = str(installed)
    repo = root / 'project'
    repo.mkdir()
    config = home / 'hooks.gitconfig'
    env = {k:v for k,v in os.environ.items() if not k.startswith(('GIT_','XDG_','WORKTREE_'))}
    env.update(HOME=str(home), XDG_STATE_HOME=str(home/'state[1]'), GIT_CONFIG_GLOBAL=str(config), GIT_CONFIG_NOSYSTEM='1', PATH=str(Path(git).parent)+os.pathsep+env['PATH'])
    def run(*args, ok=True):
        result = subprocess.run([str(a) for a in args], cwd=repo, env=env, text=True, capture_output=True, timeout=90)
        assert (result.returncode == 0) == ok, (args, result.returncode, result.stdout, result.stderr)
        return result
    run(binary,'hooks','--file',config)
    printed = home / 'printed.gitconfig'
    printed.write_text(run(binary, 'hooks').stdout)
    for event in ('pre', 'post', 'checkout'):
        key = f'hook.worktree-manager-{event}.command'
        explicit = run(git, 'config', '--file', config, '--get', key).stdout
        assert run(git, 'config', '--file', printed, '--get', key).stdout == explicit
    env['GIT_CONFIG_GLOBAL'] = str(printed)

    run(git,'init','-q','--initial-branch=main')
    run(git,'-c','user.name=Hooks','-c','user.email=hooks@example.invalid','commit','--allow-empty','-qm','initial')
    custom = root/'custom-hooks'
    custom.mkdir()
    log = root/'hook-log'
    script = custom/'post-checkout'
    script.write_text('#!/bin/sh\nprintf "called\\n" >> "'+str(log)+'"\n')
    script.chmod(0o755)
    run(git,'config','core.hooksPath',custom)
    first = root/'first'
    run(git,'worktree','add','-b','first',first)
    assert log.read_text() == 'called\n'
    second = root/'second'
    run(git,'worktree','add','--no-checkout','-b','second',second)
    report = json.loads(run(binary,'status','--json').stdout)['result']
    assert report['complete'] and report['totals']['linked_count'] == '2'
    run(binary,'limits','--max-worktrees','2')
    blocked = root/'blocked'
    run(git,'-c','user.name=Hooks','-c','user.email=hooks@example.invalid','commit','--allow-empty','-qm','advance')
    original_branch = run(git,'rev-parse','first').stdout
    denied = run(git,'worktree','add','--force','-B','first',blocked,ok=False)
    assert 'refusing add' in denied.stderr and not blocked.exists(), (denied.stdout, denied.stderr)
    assert run(git,'rev-parse','first').stdout == original_branch
    run(git,'worktree','remove',second)
    inventory = json.loads((repo/'.git/worktree-policy/inventory.json').read_text())
    assert inventory['linked_count']==1
    run(git,'worktree','add','--no-checkout','-b','third',root/'third')
    assert log.read_text() == 'called\n'
    print('Configured hooks: external tracking, no-checkout, veto, removal and custom-hook preservation passed')
