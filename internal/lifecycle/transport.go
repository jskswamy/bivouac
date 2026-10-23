package lifecycle

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/state"
	"golang.org/x/crypto/ssh/knownhosts"
)

func sshEndpointOptions(endpoint string) []string {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil {
		host = endpoint
		port = "22"
	}
	return []string{"-o", "HostName=" + host, "-o", "HostKeyAlias=" + knownhosts.Normalize(net.JoinHostPort(host, port)), "-o", "Port=" + port}
}
func sshCommand(options []string) string {
	words := []string{"ssh"}
	for _, option := range options {
		words = append(words, shellcmd.Quote(option))
	}
	return strings.Join(words, " ")
}

// instanceTransport validates recorded references and effective settings before
// a bivouac-owned external operation, without changing unrelated SSH hosts.
func instanceTransport(ctx context.Context, host, user string) (string, []string, error) {
	alias, err := sshidentity.TransportHost(ctx, host, user)
	if err != nil {
		return "", nil, err
	}
	connection, selected := sshidentity.FromContext(ctx)
	if !selected {
		return alias, []string{"-o", "BatchMode=yes"}, nil
	}
	_, cleanup, err := sshidentity.Signers(ctx, connection.Keys)
	if err != nil {
		return "", nil, err
	}
	cleanup()
	store, err := sshidentity.DefaultProfileStore()
	if err != nil {
		return "", nil, err
	}
	if err := store.Upsert(ctx, connection); err != nil {
		return "", nil, err
	}
	var options []string
	if !connection.UseIAP && host != connection.PreferredHost {
		options = sshEndpointOptions(host)
	}
	return alias, options, nil
}

func instanceGitURL(ctx context.Context, user, address, path string) (string, error) {
	alias, err := sshidentity.TransportHost(ctx, address, user)
	if err != nil {
		return "", err
	}
	if _, selected := sshidentity.FromContext(ctx); !selected {
		return sshGitURL(user, address, path), nil
	}
	return (&url.URL{Scheme: "ssh", User: url.User(user), Host: alias, Path: path}).String(), nil
}
func gitEndpoint(ctx context.Context, gitURL string) (string, error) {
	parsed, err := url.Parse(gitURL)
	if err != nil {
		return "", err
	}
	if c, selected := sshidentity.FromContext(ctx); selected && parsed.Hostname() == c.Alias {
		return dialTarget(sshGitURL(c.User, c.PreferredHost, "/"))
	}
	return dialTarget(gitURL)
}

func gitTransferTarget(args []string) (string, error) {
	for i, arg := range args {
		if arg == "push" || arg == "fetch" || arg == "ls-remote" {
			for _, target := range args[i+1:] {
				if !strings.HasPrefix(target, "-") {
					return target, nil
				}
			}
		}
	}
	return "", fmt.Errorf("instance Git transport requires a push, fetch or ls-remote target")
}

// runInstanceGit scopes its SSH override to this transfer only. Persistent
// remotes keep the alias, so ordinary Git does not need this process to exist.
func runInstanceGit(ctx context.Context, localRepo, endpoint string, args ...string) (string, error) {
	c, selected := sshidentity.FromContext(ctx)
	if !selected {
		return runLocalGit(ctx, localRepo, args...)
	}
	_, commandSet := os.LookupEnv("GIT_SSH_COMMAND")
	_, sshSet := os.LookupEnv("GIT_SSH")
	if commandSet || sshSet {
		return "", fmt.Errorf("GIT_SSH_COMMAND/GIT_SSH conflicts with selected instance transport; unset it for bivouac transfers")
	}
	if command, err := runLocalGit(ctx, localRepo, "config", "--get", "core.sshCommand"); err == nil && trimLine(command) != "" {
		return "", fmt.Errorf("core.sshCommand conflicts with selected instance transport; scope it away from this repository before transferring")
	}
	target, err := gitTransferTarget(args)
	if err != nil {
		return "", err
	}
	gitURL := target
	remote := !strings.HasPrefix(target, "ssh://")
	if remote {
		if !strings.HasPrefix(target, "bivouac-") {
			return "", fmt.Errorf("refusing selected-key override for unrelated Git remote %q", target)
		}
		gitURL, err = runLocalGit(ctx, localRepo, "remote", "get-url", target)
		if err != nil {
			return "", err
		}
		gitURL = trimLine(gitURL)
	}
	parsed, err := url.Parse(gitURL)
	if err != nil {
		return "", err
	}
	publicHost, _, splitErr := net.SplitHostPort(c.PublicIP)
	if splitErr != nil {
		publicHost = c.PublicIP
	}
	preferredHost, _, splitErr := net.SplitHostPort(c.PreferredHost)
	if splitErr != nil {
		preferredHost = c.PreferredHost
	}
	if parsed.Scheme != "ssh" || parsed.User == nil || parsed.User.Username() != c.User || parsed.Hostname() != c.Alias && parsed.Hostname() != publicHost && parsed.Hostname() != preferredHost {
		return "", fmt.Errorf("git URL %q does not belong to selected instance %s; unrelated remotes are unchanged", gitURL, c.Name)
	}
	if endpoint == "" {
		endpoint = c.PreferredHost
	}
	if endpoint != c.PreferredHost && endpoint != c.PublicIP {
		return "", fmt.Errorf("git endpoint %q is outside selected instance", endpoint)
	}
	primary, err := dialTarget(sshGitURL(c.User, endpoint, "/"))
	if err != nil {
		return "", err
	}
	if endpoint != c.PublicIP && !reachable(primary, probeTimeout) {
		fallback, err := dialTarget(sshGitURL(c.User, c.PublicIP, "/"))
		if err != nil || !reachable(fallback, probeTimeout) {
			return "", fmt.Errorf("neither preferred nor public instance SSH endpoint answered")
		}
		endpoint = c.PublicIP
	}
	_, _, err = instanceTransport(ctx, endpoint, c.User)
	if err != nil {
		return "", err
	}
	aliasURL, err := instanceGitURL(ctx, c.User, endpoint, parsed.Path)
	if err != nil {
		return "", err
	}
	effectiveURL, err := runLocalGit(ctx, localRepo, "ls-remote", "--get-url", aliasURL)
	if err != nil {
		return "", err
	}
	if trimLine(effectiveURL) != aliasURL {
		return "", fmt.Errorf("git URL rewrite changes the instance alias; scope url.*.insteadOf away from bivouac remotes before transferring")
	}
	var clean []string
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" && i+1 < len(args) && args[i+1] == fastSSHCommand {
			i++
			continue
		}
		arg := args[i]
		if !remote && arg == target {
			arg = aliasURL
		}
		clean = append(clean, arg)
	}
	options := append([]string{"-o", "ConnectTimeout=2", "-o", "BatchMode=yes"}, sshEndpointOptions(endpoint)...)
	prefix := []string{"-c", "core.sshCommand=" + sshCommand(options)}
	if remote {
		prefix = append(prefix, "-c", "remote."+target+".url="+aliasURL)
	}
	return runLocalGit(ctx, localRepo, append(prefix, clean...)...)
}

// RefreshTailnetRoute records a discovered tailnet endpoint with the same
// selected identities after the caller has recorded a successful join.
func RefreshTailnetRoute(ctx context.Context) error {
	c, selected := sshidentity.FromContext(ctx)
	if !selected {
		return nil
	}
	host, err := TailscaleIP(ctx, c.PublicIP, c.User)
	if err != nil {
		return err
	}
	_, err = rememberPreferredHost(ctx, host)
	return err
}

// rememberPreferredHost persists routing without changing authorized identities.
// An address is data, not a new credential selection from current config.
func rememberPreferredHost(ctx context.Context, host string) (context.Context, error) {
	c, selected := sshidentity.FromContext(ctx)
	if !selected || host == "" || host == c.PreferredHost {
		return ctx, nil
	}
	c.PreferredHost = host
	if c.UseIAP {
		c.UseIAP = false
	}
	profiles, err := sshidentity.DefaultProfileStore()
	if err != nil {
		return ctx, err
	}
	if err := profiles.Check(ctx, c); err != nil {
		return ctx, err
	}
	store, err := state.Open()
	if err != nil {
		return ctx, err
	}
	record, ok, err := store.Get(c.Name)
	if err != nil {
		return ctx, err
	}
	if !ok || record.SSH == nil || record.SSH.Alias != c.Alias {
		return ctx, fmt.Errorf("cannot update routing for untracked selected instance %s", c.Name)
	}
	if err := store.Update(record.Name, func(latest *state.Record) error {
		if latest.SSH == nil || latest.SSH.Alias != c.Alias {
			return fmt.Errorf("cannot update routing for changed selected instance %s", c.Name)
		}
		latest.SSH = &c
		return nil
	}); err != nil {
		return ctx, err
	}
	if err := profiles.Upsert(ctx, c); err != nil {
		return ctx, fmt.Errorf("route recorded but SSH profile update failed; retry provision: %w", err)
	}
	return sshidentity.WithConnection(ctx, c), nil
}
