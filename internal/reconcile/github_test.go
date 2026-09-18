package reconcile

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/secrets"
	"github.com/jskswamy/bivouac/internal/testenv"
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
	fakeGitHubAPI(t, 200, `{"login":"octocat"}`)

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
	if !strings.Contains(got, "not bivouac's symlink") {
		t.Errorf("errOut = %q, want it to explain ~/.config/gh is foreign", got)
	}
	for _, cmd := range commands {
		if strings.Contains(cmd, "install") || strings.Contains(cmd, "hosts.yml") {
			t.Errorf("commands = %v, want no write attempted once NOTASYMLINK is seen", commands)
		}
	}
}

// writeGitHubTokenSecretsFixture generates a fresh age identity, points
// SOPS_AGE_KEY_FILE and the isolated home at temp locations, and writes a
// real sops-encrypted secrets.yaml containing github_token -- exactly what
// placeGitHubToken decrypts in production. The age-keygen boilerplate is
// duplicated from internal/lifecycle/tailscale_test.go's
// writeTailscaleSecretsFixture rather than shared; see that function's own
// comment for why.
func writeGitHubTokenSecretsFixture(t *testing.T, token string) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "age-key.txt")
	if out, err := exec.Command("age-keygen", "-o", keyPath).CombinedOutput(); err != nil {
		t.Fatalf("age-keygen: %v\n%s", err, out)
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	var recipient string
	for _, line := range strings.Split(string(keyData), "\n") {
		if strings.HasPrefix(line, "# public key: ") {
			recipient = strings.TrimPrefix(line, "# public key: ")
		}
	}
	if recipient == "" {
		t.Fatalf("couldn't find public key in age-keygen output:\n%s", keyData)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyPath)
	testenv.Isolate(t)

	plainPath := filepath.Join(t.TempDir(), "plain.yaml")
	if err := os.WriteFile(plainPath, []byte("github_token: "+token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sops", "--age", recipient, "-e", plainPath).CombinedOutput()
	if err != nil {
		t.Fatalf("sops -e: %v\n%s", err, out)
	}
	secretsPath, err := secrets.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(secretsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretsPath, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPlaceGitHubToken_WritesHostsYAMLWhenTokenIsConfigured(t *testing.T) {
	fakeGitHubAPI(t, 200, `{"login":"octocat"}`)
	startFakeAgent(t)
	testenv.Isolate(t)
	writeGitHubTokenSecretsFixture(t, "ghp_exampletoken123")

	var commands []string
	var stdins [][]byte
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		stdins = append(stdins, stdin)
		if strings.Contains(cmd, "XDG_RUNTIME_DIR") {
			return "/run/user/1000", 0
		}
		return "", 0
	})
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	placeGitHubToken(ctx, client)

	if errOut.Len() != 0 {
		t.Fatalf("errOut = %q, want no warnings on a clean write", errOut.String())
	}
	if len(commands) != 3 {
		t.Fatalf("commands = %v, want 3 (resolve runtime dir, prepare symlink, write hosts.yml)", commands)
	}
	if !strings.Contains(commands[1], "config/gh") {
		t.Errorf("commands[1] = %q, want it to prepare ~/.config/gh", commands[1])
	}
	wantYAML := hostsYAML("octocat", []byte("ghp_exampletoken123"))
	if string(stdins[2]) != wantYAML {
		t.Errorf("stdin to the write step = %q, want %q", stdins[2], wantYAML)
	}
	if !strings.Contains(commands[2], "/run/user/1000/bivouac/gh/hosts.yml") {
		t.Errorf("commands[2] = %q, want it to write into the resolved runtime dir", commands[2])
	}
	// The token must only ever travel over stdin: a command line is visible
	// in the instance's process table to anyone who looks.
	for i, cmd := range commands {
		if strings.Contains(cmd, "ghp_exampletoken123") {
			t.Errorf("commands[%d] = %q contains the token literal -- it must only travel via stdin", i, cmd)
		}
	}
}

// fakeGitHubAPI points githubAPIBase at a test server that answers
// GET /user with status and body, and returns the Authorization headers it
// saw so a test can prove the token was actually presented.
func fakeGitHubAPI(t *testing.T, status int, body string) *[]string {
	t.Helper()
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if r.URL.Path != "/user" {
			t.Errorf("request path = %q, want /user", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := githubAPIBase
	githubAPIBase = srv.URL
	t.Cleanup(func() { githubAPIBase = old })
	return &auths
}

// TestPlaceGitHubTokenValue_NamesTheAccountInHostsYAML covers what a bare
// oauth_token cannot: gh refuses to run at all against a hosts.yml with no
// account name under it, aborting every command with a multi-account
// migration error rather than falling back to unauthenticated.
func TestPlaceGitHubTokenValue_NamesTheAccountInHostsYAML(t *testing.T) {
	auths := fakeGitHubAPI(t, 200, `{"login":"octocat"}`)

	var commands []string
	var stdins [][]byte
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		stdins = append(stdins, stdin)
		if strings.Contains(cmd, "XDG_RUNTIME_DIR") {
			return "/run/user/1000", 0
		}
		return "", 0
	})
	startFakeAgent(t)
	testenv.Isolate(t)
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	placeGitHubTokenValue(ctx, client, []byte("ghp_exampletoken123"))

	if errOut.Len() != 0 {
		t.Fatalf("errOut = %q, want no warnings on a clean write", errOut.String())
	}
	if len(*auths) != 1 || (*auths)[0] != "Bearer ghp_exampletoken123" {
		t.Errorf("Authorization headers = %v, want exactly one bearing the token", *auths)
	}
	want := "github.com:\n" +
		"    users:\n" +
		"        octocat:\n" +
		"            oauth_token: ghp_exampletoken123\n" +
		"    user: octocat\n" +
		"    oauth_token: ghp_exampletoken123\n" +
		"    git_protocol: https\n"
	if got := string(stdins[len(stdins)-1]); got != want {
		t.Errorf("hosts.yml = %q, want %q", got, want)
	}
}

func TestPlaceGitHubTokenValue_SkipsWhenGitHubRejectsTheToken(t *testing.T) {
	fakeGitHubAPI(t, 401, `{"message":"Bad credentials"}`)

	var commands []string
	addr := startFakeSSHServer(t, func(cmd string, stdin []byte) (string, uint32) {
		commands = append(commands, cmd)
		return "", 0
	})
	startFakeAgent(t)
	testenv.Isolate(t)
	var out, errOut bytes.Buffer
	ctx := provider.WithOutput(context.Background(), &out, &errOut)

	client, err := Connect(context.Background(), addr, "devuser")
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer func() { _ = client.Close() }()

	placeGitHubTokenValue(ctx, client, []byte("ghp_expired"))

	if !strings.Contains(errOut.String(), "unauthenticated") {
		t.Errorf("errOut = %q, want a warning that gh is left unauthenticated", errOut.String())
	}
	if len(commands) != 0 {
		t.Errorf("commands = %v, want the instance left untouched when the token is rejected", commands)
	}
}
