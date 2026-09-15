package agentcontext

import "fmt"

// RemoteFiles is the part of an SSH client this package needs.
//
// An interface rather than *reconcile.Client so the splice, the harness
// mapping and the idempotence are all exercised without a network. The
// client is the one thing no in-process harness can stand in for, so it
// is given nothing to decide.
type RemoteFiles interface {
	// ReadFile returns the file's contents, or "" if it does not exist.
	ReadFile(path string) (string, error)
	WriteFile(path, content string) error
}

// Write puts the rendered block into each configured harness's global
// instruction file, and reports the harnesses that have none.
//
// Paths are relative to the remote home directory; the caller's WriteFile
// places them.
func Write(rw RemoteFiles, agents []string, files []string) (unreachable []string, err error) {
	paths, unreachable := GlobalPaths(agents)
	if len(paths) == 0 {
		return unreachable, nil
	}

	block, err := Render(files)
	if err != nil {
		return unreachable, err
	}

	for _, path := range paths {
		existing, err := rw.ReadFile(path)
		if err != nil {
			return unreachable, fmt.Errorf("reading %s on the instance: %w", path, err)
		}
		if err := rw.WriteFile(path, Splice(existing, block)); err != nil {
			return unreachable, fmt.Errorf("writing %s on the instance: %w", path, err)
		}
	}
	return unreachable, nil
}
