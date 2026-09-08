package beads

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// instanceRunner is the shape of reconcile.Client.Run: run a command on the
// instance, get its combined output back. *reconcile.Client satisfies it
// structurally, so every caller keeps passing one unchanged; the interface
// exists purely as a seam so Bootstrap, Pull and Unpulled -- otherwise only
// exercisable against a live SSH connection -- can be driven by a fake in
// tests. That matters most for Unpulled: its safety contract, that (false,
// nil) means a sync demonstrably completed, is exactly what callers rely on
// before destroying a session, and untested code guarding a destructive
// action is the wrong kind of code to leave untested.
type instanceRunner interface {
	Run(cmd string) (output string, err error)
}

// Wired reports whether beads was set up for this session on this machine.
//
// The presence of the session's dolt remote is the whole answer, and it is a
// better one than cloudlab.pkl could give: pull, merge, delete and down never
// resolve a config -- down runs from any directory -- and a session started
// while beads was off must stay off for the rest of its life regardless of
// what the file says now. Same principle the spec applies to mode detection,
// one level further down.
//
// False on any failure, including bd being absent. Callers use it to skip.
func Wired(ctx context.Context, localRepo, session string) bool {
	if !Present(localRepo) || !Available() {
		return false
	}
	out, err := run(ctx, localRepo, remoteListArgs()...)
	if err != nil {
		return false
	}
	for _, r := range parseRemoteList(out) {
		if r.Name == RemoteName(session) {
			return true
		}
	}
	return false
}

// Seed registers the session's dolt remote on this machine and publishes the
// issue database into it.
//
// Must run after the session branch has been pushed and checked out: pushing
// dolt data to a git remote with no branches fails outright with "git remote
// has no branches ... initialize the repository with an initial branch/commit
// first". seedSession's existing init -> push -> checkout order already
// satisfies that, so beads work simply goes last.
//
// Re-adding an existing remote is an error and a session may be started again
// after a failed attempt, so any stale one is dropped first -- the same shape
// trackSession uses for the git remote.
func Seed(ctx context.Context, localRepo, session, url string) error {
	remote := RemoteName(session)
	_, _ = run(ctx, localRepo, removeRemoteArgs(remote)...)
	if out, err := run(ctx, localRepo, addRemoteArgs(remote, url)...); err != nil {
		return fmt.Errorf("registering dolt remote %s: %w\n%s", remote, err, out)
	}
	if out, err := run(ctx, localRepo, pushArgs(remote)...); err != nil {
		return fmt.Errorf("seeding issues into session %s: %w\n%s", session, err, out)
	}
	return nil
}

// ErrInitFailed marks a Bootstrap failure in the instance-side `bd init`
// step, meaning the instance ended up with no .beads/ database at all.
//
// Callers use errors.Is against this to tell that apart from a failure in
// the later "dolthub" remote-add step, where init already succeeded and a
// real database exists on the instance -- the two failures call for
// different responses from a caller deciding whether the session is still
// wired.
var ErrInitFailed = errors.New("initialising issues on the instance")

// ErrBdMissing narrows ErrInitFailed to the one cause a caller can hand the
// user an instruction for: the instance has no `bd` on PATH at all. It is
// always wrapped alongside ErrInitFailed, never instead of it -- the remote
// still has to be dropped exactly as for any other init failure.
//
// This is the ordinary state of any instance provisioned before beads was
// added to the templates flake, so it is worth telling apart from a bd that
// ran and failed on its own terms, where reinstalling it fixes nothing.
var ErrBdMissing = errors.New("no bd installed on the instance")

// bdMissing reads the shell's "no such command" complaint out of a failed
// init's output.
//
// Matched on the output rather than on exit status 127: the status is only
// reachable by type-asserting *ssh.ExitError, which would drag the ssh
// package into this one and, worse, past instanceRunner -- the seam that
// lets every function here be driven by a fake. A fake cannot produce an
// *ssh.ExitError, so the check would be the one thing untestable in a file
// built around being testable.
//
// Two phrasings cover the shells an instance might log in with: bash and zsh
// both say "command not found", dash and sh say "<name>: not found". The
// instance's login shell is not cloudlab's to choose, and a shell that
// phrases it a third way simply falls through to the generic message.
func bdMissing(out string) bool {
	return strings.Contains(out, "command not found") || strings.Contains(out, "bd: not found")
}

// Bootstrap clones the seeded database into the instance's checkout, and in
// "dolthub" mode adds the external remote as a second destination afterwards.
//
// The bootstrap itself always runs from fileURL, never from the external
// remote: one bootstrap path in both modes -- local, offline, needing no
// credential -- for a database the session repository already holds.
// Bootstrapping from DoltHub instead would make session start depend on
// network reachability and on a credential.
//
// externalURL empty means session mode; nothing else about this changes.
//
// The two steps fail distinguishably on purpose: wrap the init failure in
// ErrInitFailed so a caller can tell "no database on the instance at all"
// apart from "the database is fine, only the extra external remote didn't
// get added".
func Bootstrap(client instanceRunner, repo, fileURL, externalURL string) error {
	if out, err := client.Run(initCmd(repo, fileURL)); err != nil {
		if bdMissing(out) {
			return fmt.Errorf("%w: %w: %w\n%s", ErrInitFailed, ErrBdMissing, err, out)
		}
		return fmt.Errorf("%w: %w\n%s", ErrInitFailed, err, out)
	}
	if externalURL == "" {
		return nil
	}
	if out, err := client.Run(remoteAddCmd(repo, "dolthub", externalURL)); err != nil {
		return fmt.Errorf("adding the external dolt remote on the instance: %w\n%s", err, out)
	}
	return nil
}

// Pull brings the agent's issue edits home: the instance publishes them into
// the session repository's own refs/dolt/data, and this machine fetches and
// merges from there over the SSH remote it already has.
//
// Conflicts surface from `bd dolt pull` and are reported, never
// auto-resolved.
func Pull(ctx context.Context, localRepo, session string, client instanceRunner, repo string) error {
	if out, err := client.Run(pushCmd(repo)); err != nil {
		return fmt.Errorf("publishing the agent's issues on the instance: %w\n%s", err, out)
	}
	if out, err := run(ctx, localRepo, pullArgs(RemoteName(session))...); err != nil {
		return fmt.Errorf("pulling issues from session %s: %w\n%s", session, err, out)
	}
	return nil
}

// Unpulled reports whether the instance still holds issue work this machine
// does not have.
//
// It answers by performing the sync rather than by comparing refs: this
// machine's database lives in .beads/embeddeddolt and has no local
// refs/dolt/data to diff against, so there is nothing to compare. A round
// trip that demonstrably completed is the evidence -- and it is exactly the
// guarantee the callers need, since anything they could not establish they
// must treat as unsafe.
//
// Returns (false, nil) only when both halves succeeded. Every failure is an
// error, and its callers refuse to destroy on one.
func Unpulled(ctx context.Context, localRepo, session string, client instanceRunner, repo string) (bool, error) {
	if err := Pull(ctx, localRepo, session, client, repo); err != nil {
		return true, err
	}
	return false, nil
}

// InstanceVersion reads the instance's `bd version` banner, for comparison
// against this machine's own (see Version). Behind instanceRunner like
// Bootstrap, Pull and Unpulled, so the comparison can be driven by a fake in
// tests rather than only against a live SSH connection.
func InstanceVersion(client instanceRunner, repo string) (string, error) {
	out, err := client.Run(versionCmd(repo))
	if err != nil {
		return "", fmt.Errorf("reading the instance's bd version: %w\n%s", err, out)
	}
	return out, nil
}
