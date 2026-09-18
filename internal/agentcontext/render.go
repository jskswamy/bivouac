// Package agentcontext builds the instructions bivouac delivers to the
// coding agents on an instance.
//
// Two kinds of content, and only one is bivouac's. bivouac authors the
// session facts, because it is the only thing that knows them and they
// are true of every session. The workflow is the user's, named by the
// config's instructions field and transported verbatim -- shipping a
// workflow of bivouac's own would push one harness's plugins on users
// running another.
package agentcontext

import (
	"fmt"
	"os"
	"strings"
)

// The managed block's delimiters. Everything between them belongs to
// bivouac and is replaced wholesale on every write; everything outside
// belongs to whoever put it there and is preserved.
const (
	BeginMarker = "<!-- bivouac:begin -->"
	EndMarker   = "<!-- bivouac:end -->"
)

// sessionFacts is what only bivouac can say. The closing paragraph
// matters as much as the rest: these files load before a project's own,
// so without it bivouac's generic advice reads as outranking the
// repository's actual conventions.
const sessionFacts = `# Working in a bivouac session

You are a coding agent on an ephemeral cloud VM created by bivouac.
Your checkout is at ~/sessions/<name>/<repo> on branch bivouac/<name>.
Run ` + "`pwd`" + ` and ` + "`git branch --show-current`" + ` for the actual values.

- **Do not push anywhere.** You have no credentials, no GitHub access
  and no network git remote.
- **Commits are what survive.** Work returns to the user's machine via
  ` + "`bivouac session pull`" + ` and ` + "`bivouac session merge`" + `, run from their
  end. Uncommitted changes are swept into a checkpoint commit, so commit
  deliberately rather than relying on that.
- **Anything untracked gets committed** by that checkpoint (` + "`git add -A`" + `).
  Leave no scratch files, archives or databases in the working tree.
- The VM is destroyed on ` + "`bivouac down`" + `. Nothing outside the repository
  survives.

## What this session is for

1. If ` + "`../TASK.md`" + ` exists beside your checkout, read it -- that is what
   you were started for.
2. Otherwise, if the repository uses beads, check ` + "`bd list --status in_progress`" + `,
   then ` + "`bd ready`" + `.
3. Otherwise, ask before starting work.

The repository's own AGENTS.md, CLAUDE.md or CONTRIBUTING.md still apply
and win wherever they disagree with this file.
`

// Render builds the managed block: bivouac's session facts, then each
// named file's contents verbatim.
func Render(files []string) (string, error) {
	var b strings.Builder
	b.WriteString(BeginMarker)
	b.WriteString("\n")
	b.WriteString(sessionFacts)

	for _, path := range files {
		content, err := os.ReadFile(path) // #nosec G304 -- the user named this file in their own config
		if err != nil {
			return "", fmt.Errorf("reading instructions %s: %w", path, err)
		}
		b.WriteString("\n")
		b.Write(content)
		if !strings.HasSuffix(string(content), "\n") {
			b.WriteString("\n")
		}
	}

	b.WriteString(EndMarker)
	b.WriteString("\n")
	return b.String(), nil
}

// Splice puts block into existing, replacing a previous managed block if
// there is one and appending it otherwise.
//
// Replacing rather than appending is what makes repeated writes safe:
// reconcile runs on every up and provision, and session start writes
// again, so an appending implementation would grow the file without
// bound.
func Splice(existing, block string) string {
	start := lineIndex(existing, BeginMarker, 0)
	if start < 0 {
		// No managed block yet -- an end marker elsewhere in the file,
		// with no begin marker, is just text and not ours to touch.
		if existing == "" {
			return block
		}
		if !strings.HasSuffix(existing, "\n") {
			existing += "\n"
		}
		return existing + "\n" + block
	}

	// The end marker must belong to *this* begin marker, so it is only
	// searched for after it -- an end marker earlier in the file cannot
	// close a block that starts here.
	afterBegin := start + len(BeginMarker)
	if end := lineIndex(existing, EndMarker, afterBegin); end >= 0 {
		tail := existing[end+len(EndMarker):]
		return existing[:start] + strings.TrimSuffix(block, "\n") + tail
	}

	// A begin marker with nothing to close it is our own interrupted
	// write, never the user's: nothing but bivouac ever writes a begin
	// marker, and bivouac always writes both together. So everything
	// from here to EOF is a truncated bivouac block, safe to discard
	// wholesale -- content before the marker is still the user's and is
	// kept.
	return existing[:start] + block
}

// lineIndex finds the first occurrence of marker at or after from that
// stands alone on its own line, or -1.
//
// A marker has to be alone on its line to count, because a Markdown file
// may quote one as prose -- bivouac's own documentation quotes both, in
// order, and a user may well paste that into an instructions file. A
// plain substring search would take those quoted markers for a managed
// block, splice bivouac's block into the middle of the user's sentence
// and leave the real block below it untouched, so the facts would stop
// updating from then on. bivouac always writes its own markers on lines
// of their own, so nothing it wrote is missed by this.
func lineIndex(s, marker string, from int) int {
	for i := from; i <= len(s); {
		rel := strings.Index(s[i:], marker)
		if rel < 0 {
			return -1
		}
		at := i + rel
		after := at + len(marker)
		if (at == 0 || s[at-1] == '\n') && (after == len(s) || s[after] == '\n') {
			return at
		}
		i = at + 1
	}
	return -1
}
