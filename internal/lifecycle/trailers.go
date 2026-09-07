package lifecycle

import "strings"

// aiAttributionMarkers identify a trailer value that credits a machine rather
// than a person. Matched case-insensitively against the trailer's value only,
// never the prose above it -- a commit that legitimately discusses Claude Code
// or OpenAI in its body must survive untouched.
var aiAttributionMarkers = []string{
	"claude", "anthropic", "openai", "chatgpt", "gpt-", "copilot", "gemini", "noreply@",
}

// aiTrailerKeys are trailer keys that exist only to record an agent session.
// They carry no meaning once the work is on a human's branch, and the session
// URL is a private link that should not become permanent history.
var aiTrailerKeys = []string{"claude-session", "chatgpt-session", "ai-session"}

// stripAgentTrailers removes AI attribution from a commit message, leaving
// everything else byte-identical.
//
// merge re-signs every commit it replays, which is the human asserting they
// vouch for it. A signature over a message crediting an AI the signer never
// intended to credit is a false assertion -- and cherry-pick preserves messages
// verbatim, so without this the agent decides what the human's key attests to.
//
// Deliberately conservative. Only trailer LINES are considered, only in the
// trailing block, and a Co-Authored-By naming a person is kept: pairing with a
// human is real collaboration and dropping it would erase a contributor.
func stripAgentTrailers(message string) string {
	lines := strings.Split(message, "\n")
	kept := make([]string, 0, len(lines))

	for _, line := range lines {
		key, value, isTrailer := splitTrailer(line)
		if !isTrailer {
			kept = append(kept, line)
			continue
		}
		if isAITrailerKey(key) || (strings.EqualFold(key, "co-authored-by") && namesAMachine(value)) {
			continue
		}
		kept = append(kept, line)
	}

	// Removing a trailer can leave the blank line that separated it, or strand
	// several at the end. Collapse those, then restore the single terminating
	// newline git expects.
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, "\n") + "\n"
}

// splitTrailer reports whether line has the shape "Key: value", with a key
// containing no spaces -- git's own trailer shape. Prose that happens to
// contain a colon is not a trailer.
func splitTrailer(line string) (key, value string, ok bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	key = line[:idx]
	if strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, strings.TrimSpace(line[idx+1:]), true
}

func isAITrailerKey(key string) bool {
	lower := strings.ToLower(key)
	for _, k := range aiTrailerKeys {
		if lower == k {
			return true
		}
	}
	return false
}

func namesAMachine(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range aiAttributionMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
