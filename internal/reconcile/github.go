package reconcile

import (
	"context"
	"strings"

	"github.com/jskswamy/cloudlab/internal/provider"
	"github.com/jskswamy/cloudlab/internal/secrets"
	"github.com/jskswamy/cloudlab/internal/shellcmd"
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
// with no cloudlab.pkl field to enable or disable it. A missing secrets
// file, or one with no github_token key, is not a warning -- unlike
// placeDoltCredential's missing-credential case, gh has a fully working
// fallback (unauthenticated, just rate-limited) rather than a
// config-declared feature it cannot deliver on.
//
// Like placeDoltCredential and JoinTailscale, this writes only into
// $XDG_RUNTIME_DIR (tmpfs): the plaintext never touches the instance's
// persistent disk, never an environment variable, never a command-line
// argument. A reboot clears it, and the recovery is `cloudlab provision`,
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

	ghDir := runtimeDir + "/cloudlab/gh"
	out, err := client.Run(shellcmd.LoginShell(symlinkPrepareScript(".config/gh", ghDir)))
	if err != nil {
		provider.ReportWarning(ctx, "gh: could not prepare ~/.config/gh on the instance: "+err.Error()+"\n"+out)
		return
	}
	if strings.Contains(out, "NOTASYMLINK") {
		provider.ReportWarning(ctx, "gh: ~/.config/gh on the instance is not cloudlab's symlink -- a real `gh auth login` is not ours to overwrite, so it is left alone and "+unauthenticatedNote)
		return
	}

	// gh's own hosts.yml, the file it already reads by default, rather than
	// GH_TOKEN in the environment: an environment variable would have to
	// reach ssh, tmux, herdr and the agent's own shell, and silently does
	// nothing for whichever one it misses. Same reasoning dolt.go records
	// for rejecting DOLT_ROOT_PATH.
	hostsYAML := "github.com:\n    oauth_token: " + string(token) + "\n    git_protocol: https\n"
	if err := client.WriteSecretFile(ghDir+"/hosts.yml", []byte(hostsYAML)); err != nil {
		provider.ReportWarning(ctx, "gh: writing the GitHub token: "+err.Error())
	}
}
