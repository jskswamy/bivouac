package lifecycle

import (
	"context"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/testenv"
)

func TestTaskText_WritesFreeTextVerbatim(t *testing.T) {
	got, err := TaskText("Implement bv5; spec in docs/foo.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Implement bv5; spec in docs/foo.md") {
		t.Errorf("TaskText() = %q", got)
	}
}

// The ID and the command that expands it, not a copy of the issue. A copy
// is stale the moment anyone edits the issue, and the agent has bd on the
// instance.
func TestTaskText_ForAnIssueNamesItAndHowToReadIt(t *testing.T) {
	got, err := TaskText("", "cloudlab-bv5")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cloudlab-bv5", "bd show cloudlab-bv5"} {
		if !strings.Contains(got, want) {
			t.Errorf("TaskText() = %q, missing %q", got, want)
		}
	}
}

// A precedence rule nobody would remember is worse than a refusal.
func TestTaskText_RefusesBothFlags(t *testing.T) {
	_, err := TaskText("some text", "cloudlab-bv5")
	if err == nil {
		t.Fatal("TaskText() error = nil, want an error")
	}
	for _, want := range []string{"--task", "--issue"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestTaskText_WithNeitherReportsNothingToWrite(t *testing.T) {
	got, err := TaskText("", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("TaskText() = %q, want empty", got)
	}
}

// WriteTask's whole point is landing beside the checkout rather than inside
// it, so this asserts the actual remote path a command reaches, not just
// that WriteTask returns nil.
func TestWriteTask_WritesBesideTheSessionDirectory(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := WriteTask(client, "devuser", "auth-refactor", "do the thing\n"); err != nil {
		t.Fatalf("WriteTask() error = %v", err)
	}

	if len(commands) == 0 {
		t.Fatal("WriteTask() sent no command")
	}
	want := SessionDir("devuser", "auth-refactor") + "/TASK.md"
	if !strings.Contains(strings.Join(commands, "\n"), want) {
		t.Errorf("commands = %v, want one naming %s", commands, want)
	}
}

// A retry that named no task or issue must not wipe what the first run
// wrote -- WriteTask's "" no-op is what this depends on, and it is asserted
// here by never letting a command reach the instance at all.
func TestWriteTask_LeavesAnExistingFileAloneWhenTextIsEmpty(t *testing.T) {
	startFakeAgent(t)
	testenv.Isolate(t)

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})
	client, err := reconcile.Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := WriteTask(client, "devuser", "auth-refactor", ""); err != nil {
		t.Fatalf("WriteTask() error = %v", err)
	}
	if len(commands) != 0 {
		t.Errorf("commands = %v, want none sent for empty text", commands)
	}
}
