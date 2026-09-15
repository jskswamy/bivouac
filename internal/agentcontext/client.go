package agentcontext

import (
	"path"

	"github.com/jskswamy/cloudlab/internal/shellcmd"
)

// Remote adapts an SSH client's Run and WriteFile to RemoteFiles, placing
// this package's home-relative paths under home on the instance.
//
// Taking the two functions rather than the client keeps this package off
// internal/reconcile, which already depends on internal/config -- and
// leaves the adapter itself testable without a connection.
//
// home is an absolute directory, not "$HOME": the client quotes every path
// it is handed, so a shell variable arrives at the remote shell as a
// literal and would create a directory actually named $HOME. Callers
// compose it from the instance's user, whose home is always /home/<user>
// (see lifecycle.RemotePath for why that is a safe assumption).
func Remote(run func(string) (string, error), write func(string, string) error, home string) RemoteFiles {
	return remote{run: run, write: write, home: home}
}

type remote struct {
	run   func(string) (string, error)
	write func(string, string) error
	home  string
}

// ReadFile returns "" when the file is not there. On a fresh instance
// none of these files exist, so an error would fail the first write.
// Every other failure also reads as empty, which is the safe direction:
// the block is rewritten wholesale, and the cost of misreading a present
// file as absent is losing hand-added content, not a broken instruction.
func (r remote) ReadFile(p string) (string, error) {
	out, err := r.run(shellcmd.Remote([]string{"cat"}, r.abs(p)))
	if err != nil {
		return "", nil
	}
	return out, nil
}

func (r remote) WriteFile(p, content string) error {
	return r.write(r.abs(p), content)
}

// abs joins home with a harness path. path rather than filepath because
// the destination is the instance, whose separator is "/" whatever this
// machine's is.
func (r remote) abs(p string) string {
	return path.Join(r.home, p)
}
