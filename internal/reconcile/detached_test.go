package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/bivouac/internal/config"
)

// fakeInstance stands in for the instance's systemd and journal, so the
// follow loop can be exercised against a dropped connection without a
// real one. It answers by the command's shape, the way a real instance
// answers by what the command does.
type fakeInstance struct {
	state    string   // the next `systemctl show` answer
	journal  []string // lines not yet handed out
	finishAt int      // `systemctl show` calls before the unit completes
	final    string   // the state it completes with
	polls    int
	started  int
	dialed   int
	dropAt   int // drop the connection on this poll (1-based); 0 never
	cursors  []string
	startCmd string
}

type fakeConn struct {
	inst *fakeInstance
	dead bool
}

func (c *fakeConn) Close() error { return nil }

func (c *fakeConn) RunContext(_ context.Context, cmd string) (string, error) {
	if c.dead {
		return "", errors.New("read tcp: operation timed out")
	}
	in := c.inst
	switch {
	case strings.Contains(cmd, "systemd-run"):
		in.started++
		in.startCmd = cmd
		if in.final != "" {
			in.state = running
		}
		return "Running as unit: bivouac-provision.service", nil
	case strings.Contains(cmd, "journalctl"):
		in.polls++
		if in.dropAt != 0 && in.polls == in.dropAt {
			c.dead = true
			in.dropAt = 0
			return "", errors.New("read tcp: operation timed out")
		}
		// The cursor is quoted once for itself and again by LoginShell;
		// the real shell undoes both, so drop the quote marks here.
		cursor := ""
		if _, after, found := strings.Cut(cmd, "--after-cursor="); found {
			cursor = strings.Fields(strings.NewReplacer(`'`, "", `\`, "").Replace(after))[0]
		}
		in.cursors = append(in.cursors, cursor)
		var out string
		if len(in.journal) > 0 {
			out, in.journal = in.journal[0], in.journal[1:]
		}
		return out + fmt.Sprintf("-- cursor: c%d\n", in.polls), nil
	case strings.Contains(cmd, "systemctl show"):
		if in.polls >= in.finishAt && in.final != "" {
			in.state = in.final
		}
		return in.state, nil
	}
	return "", nil // stop / reset-failed
}

func (in *fakeInstance) dial(context.Context) (remote, error) {
	in.dialed++
	return &fakeConn{inst: in}, nil
}

const (
	running = "InvocationID=inv1\nLoadState=loaded\nSubState=running\nResult=success\nExecMainStatus=0\n"
	exited  = "InvocationID=inv1\nLoadState=loaded\nSubState=exited\nResult=success\nExecMainStatus=0\n"
	failed  = "InvocationID=inv1\nLoadState=loaded\nSubState=failed\nResult=exit-code\nExecMainStatus=3\n"
	absent  = "LoadState=not-found\nSubState=dead\nResult=success\nExecMainStatus=0\n"
)

func testRun() detachedRun {
	return detachedRun{
		unit:   "bivouac-provision",
		poll:   time.Millisecond,
		giveUp: 200 * time.Millisecond,
	}
}

func TestDetached_StartsOnceAndStreamsEachLineOnce(t *testing.T) {
	in := &fakeInstance{state: absent, final: exited, finishAt: 2, journal: []string{"one\n", "two\n"}}
	var out bytes.Buffer
	run := testRun()
	run.dial = in.dial
	run.out = &out

	if _, err := run.run(context.Background(), "switch"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if in.started != 1 {
		t.Errorf("unit started %d times, want 1", in.started)
	}
	if got := out.String(); got != "one\ntwo\n" {
		t.Errorf("output = %q, want each journal line once and in order", got)
	}
	if in.cursors[1] != "c1" {
		t.Errorf("second poll asked after cursor %q, want c1 so lines are not repeated", in.cursors[1])
	}
}

func TestDetached_ReconnectsAfterADropAndResumesFromTheCursor(t *testing.T) {
	in := &fakeInstance{state: absent, final: exited, finishAt: 3, dropAt: 2, journal: []string{"one\n", "two\n", "three\n"}}
	var out bytes.Buffer
	run := testRun()
	run.dial = in.dial
	run.out = &out

	if _, err := run.run(context.Background(), "switch"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if in.dialed < 2 {
		t.Errorf("dialed %d times, want a redial after the connection died", in.dialed)
	}
	if in.started != 1 {
		t.Errorf("unit started %d times, want 1: a reconnect must not restart the switch", in.started)
	}
	if got := out.String(); got != "one\ntwo\nthree\n" {
		t.Errorf("output = %q, want nothing lost or repeated across the drop", got)
	}
}

func TestDetached_AttachesToASwitchAlreadyRunning(t *testing.T) {
	in := &fakeInstance{state: running, final: exited, finishAt: 1}
	run := testRun()
	run.dial = in.dial
	run.out = &bytes.Buffer{}

	if _, err := run.run(context.Background(), "switch"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if in.started != 0 {
		t.Errorf("started %d times, want 0: a second switch must not run beside the first", in.started)
	}
}

func TestDetached_FailedUnitReportsItsExitStatusAndOutput(t *testing.T) {
	in := &fakeInstance{state: absent, final: failed, finishAt: 1, journal: []string{"boom\n"}}
	run := testRun()
	run.dial = in.dial
	run.out = &bytes.Buffer{}

	output, err := run.run(context.Background(), "switch")
	if err == nil || !strings.Contains(err.Error(), "3") {
		t.Fatalf("run() error = %v, want one naming exit status 3", err)
	}
	if !strings.Contains(output, "boom") {
		t.Errorf("output = %q, want the journal so the caller can show its tail", output)
	}
}

func TestDetached_UnitThatVanishesIsAnError(t *testing.T) {
	in := &fakeInstance{state: absent}
	run := testRun()
	run.dial = in.dial
	run.out = &bytes.Buffer{}

	if _, err := run.run(context.Background(), "switch"); err == nil {
		t.Fatal("run() error = nil for a unit that was started and is gone, want an error")
	}
}

func TestDetached_GivesUpWhenTheInstanceStaysUnreachable(t *testing.T) {
	run := testRun()
	run.dial = func(context.Context) (remote, error) { return nil, errors.New("no route to host") }
	run.out = &bytes.Buffer{}

	_, err := run.run(context.Background(), "switch")
	if err == nil || !strings.Contains(err.Error(), "still be running") {
		t.Fatalf("run() error = %v, want one saying the switch may still be running", err)
	}
}

func TestNeedsAgent_OnlyForSSHFetches(t *testing.T) {
	var cfg config.Resolved
	if needsAgent(cfg) {
		t.Error("needsAgent(empty config) = true, want false")
	}
	cfg.Flakes = []config.Flake{{Url: "github:jskswamy/nix-modules"}}
	if needsAgent(cfg) {
		t.Error("needsAgent(public github flake) = true, want false")
	}
	cfg.Flakes = append(cfg.Flakes, config.Flake{Url: "git+ssh://git@github.com/me/private"})
	if !needsAgent(cfg) {
		t.Error("needsAgent(git+ssh flake) = false, want true: a detached unit cannot use the forwarded agent")
	}
}

// An SSH login gets XDG_RUNTIME_DIR from PAM; a unit started by systemd does
// not, and without it `systemctl --user` -- which activation runs to reload
// the user's services -- fails with "Failed to connect to bus".
func TestDetached_GivesTheUnitTheUsersRuntimeDirectory(t *testing.T) {
	in := &fakeInstance{state: absent, final: exited, finishAt: 1}
	run := testRun()
	run.dial = in.dial
	run.out = &bytes.Buffer{}

	if _, err := run.run(context.Background(), "switch"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"XDG_RUNTIME_DIR=/run/user/$(id -u)", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u)/bus"} {
		if !strings.Contains(in.startCmd, want) {
			t.Errorf("start command = %q, want it to set %s", in.startCmd, want)
		}
	}
}
