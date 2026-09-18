package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/secrets"
	"github.com/jskswamy/bivouac/internal/shellcmd"
)

// unauthenticatedNote is the tail of every message explaining that gh was
// left alone. Shared so the informational skips and the warning skips read
// the same way: what happens next is identical, only the reason differs.
const unauthenticatedNote = "gh will run unauthenticated (60 requests/hour, fine for public repos)"

// placeGitHubToken ships the user's GitHub token to the instance, so gh
// there can authenticate instead of running against GitHub's anonymous
// rate limit.
//
// Purely optional, gated on whether github_token decrypts successfully,
// with no bivouac.pkl field to enable or disable it. A missing secrets
// file, or one with no github_token key, is not a warning -- unlike
// placeDoltCredential's missing-credential case, gh has a fully working
// fallback (unauthenticated, just rate-limited) rather than a
// config-declared feature it cannot deliver on.
//
// Like placeDoltCredential and JoinTailscale, this writes only into
// $XDG_RUNTIME_DIR (tmpfs): the plaintext never touches the instance's
// persistent disk, never an environment variable, never a command-line
// argument. A reboot clears it, and the recovery is `bivouac provision`,
// which is already idempotent.
func placeGitHubToken(ctx context.Context, client *Client) {
	path, err := secrets.Path()
	if err != nil {
		provider.ReportProgress(ctx, "gh: no secrets file ("+err.Error()+"); "+unauthenticatedNote)
		return
	}
	token, err := secrets.Decrypt(ctx, path, "github_token")
	if err != nil {
		provider.ReportProgress(ctx, "gh: no github_token in "+path+"; "+unauthenticatedNote)
		return
	}
	defer secrets.Zero(token)

	placeGitHubTokenValue(ctx, client, token)
}

// placeGitHubTokenValue is placeGitHubToken once a token has been
// decrypted. Split out so the tests covering what happens on the instance
// can drive it directly, without a sops-encrypted fixture for every case.
func placeGitHubTokenValue(ctx context.Context, client *Client, token []byte) {
	// Before the instance is touched: a token GitHub rejects is the user's
	// to fix, and there is nothing worth placing until it is.
	login, err := githubLogin(ctx, token)
	if err != nil {
		provider.ReportWarning(ctx, "gh: GitHub did not accept the github_token ("+err.Error()+"); "+unauthenticatedNote)
		return
	}

	runtimeDir, err := client.Run(shellcmd.LoginShell(`printf '%s' "$XDG_RUNTIME_DIR"`))
	if err != nil {
		provider.ReportWarning(ctx, "gh: could not resolve the instance's runtime directory: "+err.Error()+"; "+unauthenticatedNote)
		return
	}
	runtimeDir = strings.TrimSpace(runtimeDir)
	if runtimeDir == "" {
		provider.ReportWarning(ctx, "gh: the instance has no $XDG_RUNTIME_DIR; "+unauthenticatedNote)
		return
	}

	ghDir := runtimeDir + "/bivouac/gh"
	out, err := client.Run(shellcmd.LoginShell(symlinkPrepareScript(".config/gh", ghDir)))
	if err != nil {
		provider.ReportWarning(ctx, "gh: could not prepare ~/.config/gh on the instance: "+err.Error()+"\n"+out)
		return
	}
	if strings.Contains(out, "NOTASYMLINK") {
		provider.ReportWarning(ctx, "gh: ~/.config/gh on the instance is not bivouac's symlink -- a real `gh auth login` is not ours to overwrite, so it is left alone and "+unauthenticatedNote)
		return
	}

	if err := client.WriteSecretFile(ghDir+"/hosts.yml", []byte(hostsYAML(login, token))); err != nil {
		provider.ReportWarning(ctx, "gh: writing the GitHub token: "+err.Error())
	}
}

// githubAPIBase is GitHub's API root, a variable so tests can point
// githubLogin at a local server instead.
var githubAPIBase = "https://api.github.com"

// githubLogin asks GitHub which account token belongs to.
//
// Needed because gh will not accept a hosts.yml that names no account: on
// finding one it runs its multi-account migration, which resolves the name
// over the API itself and, when that fails, aborts *every* gh command with
// "cowardly refusing to continue with multi account migration" rather than
// falling back to unauthenticated. Resolving the name here instead means
// the instance gets a config gh accepts as it stands -- and that a token
// GitHub rejects is reported on the machine whose user can fix it, rather
// than surfacing later as that error on the instance.
//
// The design spec anticipated this ("if gh turns out to want it, the fix
// is resolving it with one extra call before shipping the token, not a
// design change"); this is that call, made directly rather than by running
// a gh bivouac does not require locally.
func githubLogin(ctx context.Context, token []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPIBase+"/user", nil)
	if err != nil {
		return "", err
	}
	// Authorization header, not a query parameter: a URL is logged by
	// proxies and stored in history in a way a header is not.
	req.Header.Set("Authorization", "Bearer "+string(token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered %s", resp.Status)
	}

	// Capped because this is an unauthenticated-so-far response being
	// parsed on the user's own machine; the real body is a few kilobytes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var account struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &account); err != nil {
		return "", err
	}
	if account.Login == "" {
		return "", fmt.Errorf("GitHub named no account")
	}
	return account.Login, nil
}

// hostsYAML renders gh's own config file, the one it already reads by
// default -- rather than GH_TOKEN in the environment, which would have to
// reach ssh, tmux, herdr and the agent's own shell and silently does
// nothing for whichever one it misses. Same reasoning dolt.go records for
// rejecting DOLT_ROOT_PATH.
//
// Both the current users-keyed shape and the older top-level one, which is
// what gh itself writes after migrating: whichever of the two a given gh
// reads, it finds the same token, and gh has no migration left to run.
func hostsYAML(login string, token []byte) string {
	return "github.com:\n" +
		"    users:\n" +
		"        " + login + ":\n" +
		"            oauth_token: " + string(token) + "\n" +
		"    user: " + login + "\n" +
		"    oauth_token: " + string(token) + "\n" +
		"    git_protocol: https\n"
}
