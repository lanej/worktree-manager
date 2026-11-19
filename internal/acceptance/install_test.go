//go:build integration

package acceptance

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func installCopy(t *testing.T, source, destination string) []byte {
	t.Helper()

	//nolint:gosec // rationale: The acceptance runner supplies its built binary or a test-owned fixture.
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // rationale: The copied executable is test-owned and must run from its temporary installation path.
	if err = os.WriteFile(destination, data, 0o755); err != nil {
		t.Fatal(err)
	}

	return data
}

func installChecksum(f *fixture, path string) {
	f.t.Helper()

	//nolint:gosec // rationale: Checksums are calculated from test-owned package fixtures.
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}

	f.write(path+".sha256", fmt.Sprintf("%x  %s\n", sha256.Sum256(data), filepath.Base(path)))
}

func TestNativeInstallation(t *testing.T) {
	f := newFixture(t)
	version := strings.TrimSpace(f.read(filepath.Join(projectRoot(t), "VERSION")))
	name := "worktree-" + runtime.GOOS + "-" + runtime.GOARCH
	fixtures := filepath.Join(f.root, "fixtures")
	f.mkdir(fixtures)

	for _, name := range []string{"uname", "awk", "sha256sum", "shasum", "cp", "sed", "mkdir", "mktemp", "rm", "basename", "chmod", "mv"} {
		tool, err := exec.LookPath(name)
		if err == nil {
			if err = os.Symlink(tool, filepath.Join(fixtures, name)); err != nil {
				t.Fatal(err)
			}
		}
	}

	curl := filepath.Join(fixtures, "curl")
	lifecycleExecutable(f, curl, "#!/bin/sh\ncase \"$2\" in\n  https://github.com/lanej/worktree-manager/releases/download/v"+version+"/*) ;;\n  *) exit 9 ;;\nesac\ncp \"$POLICY_FIXTURES/$(basename \"$2\")\" \"$4\"\n")
	packages := filepath.Join(f.root, "packages")
	f.mkdir(packages)
	packagePath := filepath.Join(packages, name)
	installCopy(t, f.binary, packagePath)
	installChecksum(f, packagePath)
	f.env["PATH"], f.env["POLICY_FIXTURES"] = fixtures, packages
	f.env["XDG_STATE_HOME"], f.env["XDG_CONFIG_HOME"] = filepath.Join(f.home, ".state"), filepath.Join(f.home, ".config")
	delete(f.env, "WORKTREE_INSTALL_BINARY")

	installer := filepath.Join(projectRoot(t), "scripts/install-worktree")
	install := func(source string, success bool) result {
		t.Helper()
		delete(f.env, "WORKTREE_INSTALL_BINARY")

		if source != "" {
			f.env["WORKTREE_INSTALL_BINARY"] = source
		}

		out := f.execute(f.repo, "/bin/sh", installer, version)
		lifecycleCheck(t, (out.code == 0) == success, "installer success=%t got %+v", success, out)

		return out
	}
	install("", true)

	destination := filepath.Join(f.home, ".local/bin/worktree")

	//nolint:gosec // rationale: Read only the executable installed in this test's temporary home.
	original, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}

	lifecycleCheck(t, strings.TrimSpace(f.run(destination, "--version").stdout) == "worktree "+version, "installed executable version mismatch")
	lifecycleExecutable(f, curl, "#!/bin/sh\nexit 8\n")
	lifecycleCheck(t, strings.Contains(install("", true).stdout, "Using installed"), "verified installation was not reused")

	bad := filepath.Join(f.root, "bad-policy")
	installCopy(t, f.binary, bad)
	f.write(bad+".sha256", strings.Repeat("0", 64)+"  "+filepath.Base(bad)+"\n")
	lifecycleCheck(t, strings.Contains(install(bad, false).stderr, "checksum mismatch"), "invalid checksum not rejected")
	lifecycleCheck(t, bytes.Equal([]byte(f.read(destination)), original), "checksum rejection changed installation")
	lifecycleExecutable(f, bad, "#!/bin/sh\necho wrong-version\n")
	installChecksum(f, bad)
	lifecycleCheck(t, strings.Contains(install(bad, false).stderr, "version or platform mismatch"), "wrong version not rejected")
	lifecycleCheck(t, bytes.Equal([]byte(f.read(destination)), original), "version rejection changed installation")
	lifecycleRemove(f, destination)
	f.write(destination, "user replacement\n")
	lifecycleCheck(t, strings.Contains(install(packagePath, false).stderr, "modified") && f.read(destination) == "user replacement\n", "modified installation overwritten")

	//nolint:gosec // rationale: Restore this test's installed executable to exercise concurrent replacement protection.
	if err = os.WriteFile(destination, original, 0o755); err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // rationale: The restored test-owned executable must run during installer verification.
	if err = os.Chmod(destination, 0o755); err != nil {
		t.Fatal(err)
	}

	lifecycleExecutable(f, curl, "#!/bin/sh\nprintf 'concurrent replacement\\n' > \"$HOME/.local/bin/worktree\"\ncp \"$POLICY_FIXTURES/$(basename \"$2\")\" \"$4\"\n")
	receipt := filepath.Join(f.home, ".local/share/worktree-manager/install-receipt")
	f.write(receipt, fmt.Sprintf("0.0.0\n%x\n", sha256.Sum256(original)))
	lifecycleCheck(t, strings.Contains(install("", false).stderr, "changed installation") && f.read(destination) == "concurrent replacement\n", "concurrent replacement was overwritten")
	lifecycleRemove(f, destination)
	f.write(destination, "user-managed binary\n")
	lifecycleRemove(f, receipt)
	lifecycleCheck(t, strings.Contains(install(packagePath, false).stderr, "unmanaged") && f.read(destination) == "user-managed binary\n", "unmanaged installation was overwritten")
	lifecycleRemove(f, destination)

	if err = os.Symlink(f.binary, destination); err != nil {
		t.Fatal(err)
	}

	lifecycleCheck(t, strings.Contains(install(packagePath, false).stderr, "unmanaged"), "unmanaged symlink was accepted")

	info, err := os.Lstat(destination)
	if err != nil {
		t.Fatal(err)
	}

	lifecycleCheck(t, info.Mode()&os.ModeSymlink != 0, "unmanaged symlink was replaced")
}
