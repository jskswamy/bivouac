package shellcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Quote is the most-depended-on symbol in this codebase -- every remote
// command in internal/beads, internal/lifecycle and internal/reconcile is
// built from it -- and before it moved here it was pinned only indirectly,
// through the builders that call it. That is not enough to move it: a
// builder test asserts the string a builder produced, so it would keep
// passing against a quoting function that had quietly stopped quoting.
//
// So these tests assert the actual contract, which is a property of the
// SHELL and not of the returned string: whatever goes in, the shell must
// hand back byte for byte, and nothing inside it may execute.

// The round trip. printf %s writes its argument with no interpretation, so
// if the quoting is correct the shell reproduces the input exactly.
func TestQuote_SurvivesTheShellUnchanged(t *testing.T) {
	cases := map[string]string{
		"plain":            "hello",
		"empty":            "",
		"spaces":           "two words",
		"single quote":     "it's here",
		"only a quote":     "'",
		"adjacent quotes":  "''",
		"quote at the end": "trailing'",
		"double quotes":    `say "hi"`,
		"backslash":        `back\slash`,
		"newline":          "one\ntwo",
		"tab":              "one\ttwo",
		"dollar":           "$HOME",
		"command sub":      "$(id)",
		"backticks":        "`id`",
		"semicolon":        "a; id",
		"pipe":             "a | id",
		"glob":             "*",
		"tilde":            "~",
		"unicode":          "café ☕",
		// A real path from the session layout, which is what this mostly
		// quotes in production.
		"session path": "/home/devuser/sessions/it's here/repo",
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			// #nosec G204 -- the whole point is to run the quoted string
			// through a real shell; the input is a test constant.
			out, err := exec.Command("bash", "-c", "printf %s "+Quote(in)).Output()
			if err != nil {
				t.Fatalf("bash rejected the quoted value: %v", err)
			}
			if string(out) != in {
				t.Errorf("round trip through the shell changed the value:\n  in  %q\n  out %q", in, string(out))
			}
		})
	}
}

// The security property, run rather than inspected. A string-shape assertion
// cannot show that an injection does not fire -- only executing it can. This
// mirrors TestRemoveRepoCmd_DoesNotExecuteInjectedCommands in
// internal/lifecycle, which guards the one command that deletes anything.
func TestQuote_DoesNotLetAnInjectedCommandRun(t *testing.T) {
	dir := t.TempDir()
	bystander := filepath.Join(dir, "keepme")
	if err := os.WriteFile(bystander, []byte("intact"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, payload := range []string{
		"x'; rm -f " + bystander + "; echo '",
		"x$(rm -f " + bystander + ")",
		"x`rm -f " + bystander + "`",
	} {
		// #nosec G204 -- deliberately executing an injection attempt to prove
		// the quoting neutralises it.
		if err := exec.Command("bash", "-c", "printf %s "+Quote(payload)).Run(); err != nil {
			t.Fatalf("bash rejected the quoted payload %q: %v", payload, err)
		}
		if _, err := os.Stat(bystander); err != nil {
			t.Fatalf("payload %q escaped its quoting and deleted the bystander: %v", payload, err)
		}
	}
}

// The output is always a single shell word, which is why callers can join
// arguments with a space and rely on the remote shell re-parsing them into
// the same argument list.
func TestQuote_ProducesOneWord(t *testing.T) {
	for _, in := range []string{"", "two words", "it's here", "a\nb", "$(id)"} {
		// echo prints one line per word only if the shell split it; counting
		// the arguments the shell actually passed is the direct check.
		script := "set -- " + Quote(in) + "; echo $#"
		// #nosec G204 -- test constant.
		out, err := exec.Command("bash", "-c", script).Output()
		if err != nil {
			t.Fatalf("bash rejected %q: %v", in, err)
		}
		if got := strings.TrimSpace(string(out)); got != "1" {
			t.Errorf("Quote(%q) became %s shell words, want 1", in, got)
		}
	}
}

// The shape every caller's test asserts on, and the reason the wrapper
// exists at all: -l, so the profile that puts the instance's binaries on
// PATH is sourced.
func TestLoginShell_RunsInnerAsALoginShell(t *testing.T) {
	got := LoginShell("git status")
	if want := "bash -lc 'git status'"; got != want {
		t.Errorf("LoginShell() = %q, want %q", got, want)
	}
}

// The wrapper's own quoting has to survive an inner command that already
// contains quotes -- which is the normal case, since inner is usually built
// out of Quote'd values.
func TestLoginShell_QuotesAnInnerCommandThatAlreadyQuotes(t *testing.T) {
	inner := "cd " + Quote("/home/it's here") + " && git status"

	// The value bash -c would receive back is the thing that matters, so
	// read it out of the shell rather than eyeballing the escaping.
	// #nosec G204 -- test constant.
	out, err := exec.Command("bash", "-c", "set -- "+strings.TrimPrefix(LoginShell(inner), "bash -lc ")+"; printf %s \"$1\"").Output()
	if err != nil {
		t.Fatalf("bash rejected the wrapped command: %v", err)
	}
	if string(out) != inner {
		t.Errorf("the inner command did not survive wrapping:\n  in  %q\n  out %q", inner, string(out))
	}
}

// Remote splices its prefix in verbatim and quotes every argument, so a
// caller can pre-quote the one prefix word that is a value.
func TestRemote_QuotesArgumentsAndKeepsThePrefixVerbatim(t *testing.T) {
	got := Remote([]string{"git", "-C", Quote("/srv/repo")}, "checkout", "main")
	if want := "bash -lc 'git -C '\\''/srv/repo'\\'' '\\''checkout'\\'' '\\''main'\\'''"; got != want {
		t.Errorf("Remote() = %q, want %q", got, want)
	}
}

// The reason arguments are quoted even when they look like fixed literals:
// the login shell re-parses the string, so an argument carrying shell
// metacharacters must arrive as one word with nothing executed.
func TestRemote_ArgumentsReachTheCommandUnchanged(t *testing.T) {
	// printf %s\n stands in for the remote command: it reports exactly the
	// argv the login shell rebuilt.
	args := []string{"commit", "-m", "fix: it's $(id) time"}
	// #nosec G204 -- test constant.
	out, err := exec.Command("bash", "-c", Remote([]string{"printf", `'%s\n'`}, args...)).Output()
	if err != nil {
		t.Fatalf("bash rejected the remote command: %v", err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(got) != len(args) {
		t.Fatalf("the shell rebuilt %d arguments, want %d: %q", len(got), len(args), got)
	}
	for i := range args {
		if got[i] != args[i] {
			t.Errorf("argument %d changed:\n  in  %q\n  out %q", i, args[i], got[i])
		}
	}
}

// No arguments is a real call shape (bd version, git status), and it must
// not leave a trailing separator behind.
func TestRemote_WithNoArguments(t *testing.T) {
	got := Remote([]string{"cd", Quote("/srv/repo"), "&&", "bd"})
	if want := "bash -lc 'cd '\\''/srv/repo'\\'' && bd'"; got != want {
		t.Errorf("Remote() = %q, want %q", got, want)
	}
}
