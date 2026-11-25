package rust

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
)

// lockVar is Cargo's setting for where it reads and writes a workspace's
// lock file (resolver.lockfile-path, stable since Cargo 1.97). An older
// Cargo ignores it.
const lockVar = "CARGO_RESOLVER_LOCKFILE_PATH"

// lockOutside points the Cargo that rust-analyzer runs at a copy of the
// workspace's Cargo.lock, so indexing never writes the repository: with
// no Cargo.lock, rust-analyzer 1.100's cargo metadata writes one into the
// tree, and with one out of date, it rewrites it (#81). Either way a file
// in the Rust layer's signature changes and the next build reruns the
// layer. The copy resolves as the original would. The returned function
// removes the copy. A lock path the environment already sets is left as
// it is.
func lockOutside(cmd *exec.Cmd, ws string) (func(), error) {
	if _, set := os.LookupEnv(lockVar); set {
		return func() {}, nil
	}
	dir, err := os.MkdirTemp("", "alidade-lock-*")
	if err != nil {
		return nil, err
	}
	done := func() { os.RemoveAll(dir) }
	lock := filepath.Join(dir, "Cargo.lock")
	b, err := os.ReadFile(filepath.Join(ws, "Cargo.lock"))
	if err == nil {
		err = os.WriteFile(lock, b, 0o644)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		done()
		return nil, err
	}
	cmd.Env = append(os.Environ(), lockVar+"="+lock)
	return done, nil
}
