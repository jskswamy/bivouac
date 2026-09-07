package beads

import (
	"context"
	"fmt"
	"strings"
)

// Mode is what a repository's beads database is currently synced against.
// Read from the repository rather than declared in cloudlab.pkl: .beads/
// already records it, and a second copy in config would only drift.
type Mode int

const (
	// ModeAbsent -- no beads database here. Every beads step is skipped.
	ModeAbsent Mode = iota
	// ModeUnsynced -- beads is present but has no dolt remote at all.
	ModeUnsynced
	// ModeGit -- synced over a git remote (git+ssh:// or git+file://).
	ModeGit
	// ModeExternal -- synced against DoltHub, Azure or Hosted Dolt.
	ModeExternal
)

func (m Mode) String() string {
	switch m {
	case ModeAbsent:
		return "absent"
	case ModeUnsynced:
		return "unsynced"
	case ModeGit:
		return "git"
	case ModeExternal:
		return "external"
	}
	return "unknown"
}

// Remote is one entry from `bd dolt remote list`.
type Remote struct {
	Name string
	URL  string
}

// Detection is what Detect found. ExternalURL is set only for ModeExternal,
// and exists so "dolthub" mode can reuse the URL the repository already
// records instead of restating it in cloudlab.pkl.
type Detection struct {
	Mode        Mode
	ExternalURL string
}

// Detect reports how repo's beads database syncs.
//
// It answers two questions and no others: whether beads exists here at all,
// and what the external remote's URL is. It never selects behaviour by
// itself -- cloudlab.pkl does that.
func Detect(ctx context.Context, repo string) (Detection, error) {
	if !Present(repo) || !Available() {
		return Detection{Mode: ModeAbsent}, nil
	}
	out, err := run(ctx, repo, remoteListArgs()...)
	if err != nil {
		return Detection{}, fmt.Errorf("bd dolt remote list in %s: %w\n%s", repo, err, out)
	}
	return classify(parseRemoteList(out)), nil
}

// remoteListArgs is the one bd invocation Detect makes.
func remoteListArgs() []string {
	return []string{"dolt", "remote", "list"}
}

// parseRemoteList reads `bd dolt remote list` output into name/URL pairs.
//
// Deliberately forgiving: any run of whitespace separates the two columns, a
// header line is skipped by the URL-shaped check below, and anything that is
// not two fields is ignored. The alternative -- a strict format -- would turn
// a cosmetic change in a tool cloudlab does not own into a failed session.
func parseRemoteList(out string) []Remote {
	var remotes []Remote
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// A URL, not a header: every dolt remote URL has a scheme.
		if !strings.Contains(fields[1], "://") {
			continue
		}
		remotes = append(remotes, Remote{Name: fields[0], URL: fields[1]})
	}
	return remotes
}

// classify turns a remote list into a Mode.
//
// External wins when a repository has both, which is the case this design was
// written in: the external URL is the one thing a caller cannot reconstruct
// for itself, whereas the session's git+ URL is built from the session name
// and the instance address it already holds.
func classify(remotes []Remote) Detection {
	if len(remotes) == 0 {
		return Detection{Mode: ModeUnsynced}
	}
	found := Detection{Mode: ModeUnsynced}
	for _, r := range remotes {
		if strings.HasPrefix(r.URL, "git+") {
			if found.Mode == ModeUnsynced {
				found.Mode = ModeGit
			}
			continue
		}
		return Detection{Mode: ModeExternal, ExternalURL: r.URL}
	}
	return found
}
