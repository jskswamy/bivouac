package reconcile

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/testenv"
)

func TestPlaceGitHubToken_SkipsCleanlyWhenNoSecretIsConfigured(t *testing.T) {
	// Progress goes to a ProgressFunc, warnings to the error writer, so
	// the test has to watch both to tell "noted, carried on" apart from
	// "something is wrong".
	var progress []string
	var out, errOut bytes.Buffer
	ctx := provider.WithProgress(provider.WithOutput(context.Background(), &out, &errOut),
		func(status string) { progress = append(progress, status) })

	// An isolated home means secrets.Path() names a file that does not
	// exist -- the ordinary "never ran secrets init, or never added this
	// key" case.
	testenv.Isolate(t)

	// A nil client would panic if anything ran, so this also proves the
	// skip never reaches the instance.
	placeGitHubToken(ctx, nil)

	if errOut.Len() != 0 {
		t.Errorf("errOut = %q, want silence: a missing optional secret is not a warning", errOut.String())
	}
	if len(progress) == 0 {
		t.Fatal("no progress reported, want an informational note that gh will run unauthenticated")
	}
	if last := progress[len(progress)-1]; !strings.Contains(last, "unauthenticated") {
		t.Errorf("last progress line = %q, want it to say gh stays unauthenticated", last)
	}
}

func TestPlaceGitHubTokenValue_LeavesAForeignConfigGhAlone(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		if strings.Contains(cmd, "XDG_RUNTIME_DIR") {
			return "/run/user/1000", 0
		}
		if strings.Contains(cmd, "config/gh") {
			return "NOTASYMLINK\n", 0
		}
		return "", 0
	})
	startFakeAgent(t)
	testenv.Isolate(t)
	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	placeGitHubTokenValue(ctx, client, []byte("ghp_example"))

	got := errOut.String()
	if !strings.Contains(got, "not cloudlab's symlink") {
		t.Errorf("errOut = %q, want it to explain ~/.config/gh is foreign", got)
	}
	for _, cmd := range commands {
		if strings.Contains(cmd, "install") || strings.Contains(cmd, "hosts.yml") {
			t.Errorf("commands = %v, want no write attempted once NOTASYMLINK is seen", commands)
		}
	}
}
