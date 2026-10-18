package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jskswamy/bivouac/internal/shellcmd"
)

// remote is the part of Client a detached run needs, so the follow loop can
// be tested against a connection that dies.
type remote interface {
	RunContext(ctx context.Context, cmd string) (string, error)
	Close() error
}

// detachedRun runs one command under systemd on the instance and follows it
// over connections it is free to lose.
//
// A command run inside an SSH session ends with the session, and a session
// ends whenever the connection does: moving between networks changes the
// source address, and a TCP connection tied to the old one goes silent
// rather than reset. Running the command as a unit makes the connection a
// window onto it instead of its parent, so a drop costs a reconnect and not
// the build.
type detachedRun struct {
	// conn is the connection already open, reused until it fails.
	conn remote
	dial func(context.Context) (remote, error)
	unit string
	out  io.Writer
	// poll paces both following and redialling; giveUp bounds how long the
	// instance may stay unreachable before run stops waiting.
	poll, giveUp time.Duration
}

// run starts inner as the unit, or attaches if the unit is already running,
// streams its output to d.out, and returns that output with an error if the
// unit failed. The unit's result is what systemd recorded, not what this
// process happened to see.
func (d *detachedRun) run(ctx context.Context, inner string) (string, error) {
	initial := d.conn
	defer func() {
		if d.conn != nil && d.conn != initial {
			_ = d.conn.Close()
		}
	}()

	if err := d.ensureStarted(ctx, inner); err != nil {
		return "", err
	}
	st, err := d.status(ctx)
	if err != nil {
		return "", err
	}
	if st["LoadState"] == "not-found" {
		return "", fmt.Errorf("unit %s is gone right after starting -- did the instance reboot?", d.unit)
	}
	// Scoped to this invocation: a unit name outlives its runs, and an
	// earlier run's lines belong to a different switch.
	filter := "_SYSTEMD_INVOCATION_ID=" + shellcmd.Quote(st["InvocationID"])

	var all bytes.Buffer
	w := io.MultiWriter(d.out, &all)
	cursor := ""
	for {
		if cursor, err = d.drain(ctx, filter, cursor, w); err != nil {
			return all.String(), err
		}
		if st, err = d.status(ctx); err != nil {
			return all.String(), err
		}
		if s := st["SubState"]; s == "exited" || s == "failed" {
			// Lines written between the last read and the unit finishing.
			if _, err = d.drain(ctx, filter, cursor, w); err != nil {
				return all.String(), err
			}
			if st["Result"] == "success" {
				return all.String(), nil
			}
			return all.String(), fmt.Errorf("%s ended with %s, status %s", d.unit, st["Result"], st["ExecMainStatus"])
		}
		if err := sleep(ctx, d.poll); err != nil {
			return all.String(), err
		}
	}
}

// ensureStarted leaves the unit running. A unit already running is the
// switch an earlier, interrupted run began, so it is attached to rather than
// duplicated. A finished one is cleared first: its name is the lock, and a
// loaded unit refuses a second start.
//
// The runtime directory and bus are set by hand: an SSH login gets them from
// PAM and a unit does not, and without them `systemctl --user`, which
// activation runs to reload the user's services, cannot reach the manager
// that cloud-init's lingering keeps running.
//
// RemainAfterExit keeps a finished unit loaded so its result outlives the
// connection; without it a success is collected at once and cannot be told
// from a unit that never ran.
func (d *detachedRun) ensureStarted(ctx context.Context, inner string) error {
	start := shellcmd.LoginShell(fmt.Sprintf(
		"sudo -n systemctl stop %[1]s 2>/dev/null; sudo -n systemctl reset-failed %[1]s 2>/dev/null; "+
			"sudo -n systemd-run --unit=%[1]s --uid=$(id -un) --working-directory=\"$HOME\" "+
			"--setenv=XDG_RUNTIME_DIR=/run/user/$(id -u) "+
			"--setenv=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$(id -u)/bus "+
			"--property=RemainAfterExit=yes bash -lc %[2]s",
		d.unit, shellcmd.Quote(inner)))
	for {
		st, err := d.status(ctx)
		if err != nil {
			return err
		}
		if st["SubState"] == "running" {
			return nil
		}
		_, err = d.exec(ctx, start)
		if err == nil {
			return nil
		}
		if isExit(err) {
			return fmt.Errorf("starting %s: %w", d.unit, err)
		}
		// The connection died with the start possibly already done. Not
		// retried blind: the next status says whether it took.
	}
}

func (d *detachedRun) status(ctx context.Context) (map[string]string, error) {
	out, err := d.retry(ctx, shellcmd.Remote([]string{"systemctl", "show", d.unit},
		"-p", "InvocationID", "-p", "LoadState", "-p", "SubState", "-p", "Result", "-p", "ExecMainStatus"))
	if err != nil {
		return nil, err
	}
	st := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			st[k] = v
		}
	}
	return st, nil
}

// drain writes the journal lines after cursor to w and returns the new
// cursor. Resuming by cursor is what makes a reconnect lose and repeat
// nothing.
func (d *detachedRun) drain(ctx context.Context, filter, cursor string, w io.Writer) (string, error) {
	cmd := "journalctl " + filter + " --no-pager -o cat --show-cursor"
	if cursor != "" {
		cmd += " --after-cursor=" + shellcmd.Quote(cursor)
	}
	out, err := d.retry(ctx, shellcmd.LoginShell(cmd))
	if err != nil {
		return cursor, err
	}
	for line := range strings.SplitAfterSeq(out, "\n") {
		switch {
		case strings.HasPrefix(line, "-- cursor: "):
			cursor = strings.TrimSpace(strings.TrimPrefix(line, "-- cursor: "))
		case line == "" || strings.HasPrefix(line, "-- No entries --"):
		default:
			if _, err := io.WriteString(w, line); err != nil {
				return cursor, err
			}
		}
	}
	return cursor, nil
}

// retry runs cmd, redialling when the connection fails, until it answers or
// the instance has been unreachable for giveUp. A command that ran and
// failed is the command's answer, not a reason to reconnect.
func (d *detachedRun) retry(ctx context.Context, cmd string) (string, error) {
	var since time.Time
	for {
		out, err := d.exec(ctx, cmd)
		if err == nil || isExit(err) || ctx.Err() != nil {
			return out, err
		}
		if since.IsZero() {
			since = time.Now()
		}
		if time.Since(since) > d.giveUp {
			return "", fmt.Errorf("lost contact with the instance (%w); the switch may still be running -- run bivouac provision to attach to it", err)
		}
		if err := sleep(ctx, d.poll); err != nil {
			return "", err
		}
	}
}

// exec is one attempt. A failure that is not the command's own drops the
// connection, so the next attempt dials a fresh one.
func (d *detachedRun) exec(ctx context.Context, cmd string) (string, error) {
	if d.conn == nil {
		c, err := d.dial(ctx)
		if err != nil {
			return "", err
		}
		d.conn = c
	}
	out, err := d.conn.RunContext(ctx, cmd)
	if err != nil && !isExit(err) {
		_ = d.conn.Close()
		d.conn = nil
	}
	return out, err
}

// isExit reports a command that ran and exited non-zero, as opposed to a
// connection that failed under it.
func isExit(err error) bool {
	_, ok := errors.AsType[*ssh.ExitError](err)
	return ok
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
