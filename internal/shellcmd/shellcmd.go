// Package shellcmd builds the command strings bivouac hands to a remote
// shell over SSH.
//
// It is deliberately a leaf: the only import is "strings". Quoting a shell
// argument used to live in internal/reconcile, which meant every package
// that needed four lines of string escaping also took on reconcile's SSH,
// home-manager, provider, secrets and state dependencies -- and could then
// never be imported back by reconcile without a cycle. Keeping the
// vocabulary here, where it depends on nothing, is what lets the packages
// that speak it depend on each other freely.
package shellcmd

import "strings"

// Quote wraps s in single quotes for safe inclusion in a remote shell
// command, escaping any embedded single quotes. Used for every value that
// isn't a fixed constant string written by this codebase.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// LoginShell wraps inner so the remote shell runs it as a login shell.
//
// bash -lc is mandatory, not stylistic: the binaries bivouac invokes on an
// instance (git, bd, tmux, herdr, tailscale) come from the instance user's
// home-manager profile (~/.nix-profile/bin), and a non-interactive SSH
// command runs a non-login shell that never sources the profile scripts
// putting that on PATH.
//
// inner is quoted as a single argument because the login shell re-parses
// the whole string. Callers that build inner from untrusted values must
// therefore Quote those values too -- the quoting nests, and this outer
// layer protects the argument to -c, not the words inside it.
//
// Note that this fixes PATH for the shell, not for whatever the shell then
// execs: a `sudo` inside inner starts a fresh environment, so commands that
// sudo still need an absolute path.
func LoginShell(inner string) string {
	return "bash -lc " + Quote(inner)
}

// Remote builds a login-shell command from a fixed prefix and a list of
// arguments, quoting every argument.
//
// prefix is spliced in verbatim so a caller can mix literals with values it
// has already quoted -- []string{"git", "-C", Quote(dir)}. args are quoted
// here, including fixed literals like "init" or "push", so there is exactly
// one escaping path to trust rather than a hand-audited allow-list standing
// next to it.
func Remote(prefix []string, args ...string) string {
	words := make([]string, 0, len(prefix)+len(args))
	words = append(words, prefix...)
	for _, a := range args {
		words = append(words, Quote(a))
	}
	return LoginShell(strings.Join(words, " "))
}
