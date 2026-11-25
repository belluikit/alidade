package rust

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLockOutside checks the lock file rust-analyzer's Cargo is given: a
// copy of the workspace's own, so that dependencies resolve as they are
// pinned, or none when the workspace has none; and nothing at all when
// the environment already names one.
func TestLockOutside(t *testing.T) {
	pinned := "# pinned\nversion = 4\n"
	for _, c := range []struct {
		name, lock, env string
		want            string // the copy's content; "-" for no copy
	}{
		{name: "a lock is copied", lock: pinned, want: pinned},
		{name: "no lock, no copy", want: "-"},
		{name: "the environment's is kept", lock: pinned, env: "/elsewhere/Cargo.lock"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := t.TempDir()
			if c.lock != "" {
				if err := os.WriteFile(filepath.Join(ws, "Cargo.lock"), []byte(c.lock), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if c.env != "" {
				t.Setenv(lockVar, c.env)
			}
			cmd := exec.Command("rust-analyzer")
			done, err := lockOutside(cmd, ws)
			if err != nil {
				t.Fatal(err)
			}
			if c.env != "" {
				done()
				if cmd.Env != nil {
					t.Errorf("the command's environment was replaced; want the process's own, where %s is %s", lockVar, c.env)
				}
				return
			}
			checkCopy(t, cmd, ws, c.want)
			done()
			if _, err := os.Stat(filepath.Dir(lockIn(t, cmd))); err == nil {
				t.Error("the copy's directory is still there after done")
			}
		})
	}
}

// lockIn is the lock path in cmd's environment.
func lockIn(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	at := slices.IndexFunc(cmd.Env, func(v string) bool { return strings.HasPrefix(v, lockVar+"=") })
	if at < 0 {
		t.Fatalf("no %s in the command's environment", lockVar)
	}
	return strings.TrimPrefix(cmd.Env[at], lockVar+"=")
}

// checkCopy checks that cmd's lock path is a Cargo.lock outside ws
// holding want, or nothing when want is "-".
func checkCopy(t *testing.T, cmd *exec.Cmd, ws, want string) {
	t.Helper()
	path := lockIn(t, cmd)
	if filepath.Base(path) != "Cargo.lock" || strings.HasPrefix(path, ws) {
		t.Errorf("lock path %s; want a Cargo.lock outside %s", path, ws)
	}
	got := "-"
	if b, err := os.ReadFile(path); err == nil {
		got = string(b)
	}
	if got != want {
		t.Errorf("copy %q; want %q", got, want)
	}
}
