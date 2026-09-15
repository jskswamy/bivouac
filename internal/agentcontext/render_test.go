package agentcontext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// With nothing configured a session still gets the facts. They are not
// opinion -- an agent that does not know commits are what survive will
// lose work -- so they are the floor, not an optional extra.
func TestRender_WithNoFilesIsFactsOnly(t *testing.T) {
	got, err := Render(nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, want := range []string{
		BeginMarker,
		EndMarker,
		"Do not push anywhere",
		"Commits are what survive",
		"../TASK.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing %q:\n%s", want, got)
		}
	}
}

// User content is transported, never interpreted: it arrives byte for
// byte, in order, after cloudlab's own block.
func TestRender_AppendsFilesVerbatimInOrder(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "a.md")
	second := filepath.Join(dir, "b.md")
	write(t, first, "# Mine\nrun `refactor:scan`\n")
	write(t, second, "# Theirs\n")

	got, err := Render([]string{first, second})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(got, "run `refactor:scan`") {
		t.Errorf("user content not delivered verbatim:\n%s", got)
	}
	if strings.Index(got, "# Mine") > strings.Index(got, "# Theirs") {
		t.Error("files delivered out of order")
	}
	if strings.Index(got, "Commits are what survive") > strings.Index(got, "# Mine") {
		t.Error("user content came before cloudlab's block")
	}
	if !strings.HasSuffix(strings.TrimSpace(got), EndMarker) {
		t.Errorf("block not closed by the end marker:\n%s", got)
	}
}

// Rewriting must not stack blocks, and must not disturb anything the
// user put in the file themselves.
func TestSplice_ReplacesInPlaceAndKeepsTheRest(t *testing.T) {
	existing := "# My own notes\nkeep me\n\n" +
		BeginMarker + "\nold content\n" + EndMarker + "\n\ntrailing\n"

	got := Splice(existing, BeginMarker+"\nnew content\n"+EndMarker)

	if strings.Contains(got, "old content") {
		t.Errorf("old block survived:\n%s", got)
	}
	if strings.Count(got, BeginMarker) != 1 {
		t.Errorf("block duplicated:\n%s", got)
	}
	for _, want := range []string{"keep me", "trailing", "new content"} {
		if !strings.Contains(got, want) {
			t.Errorf("Splice() lost %q:\n%s", want, got)
		}
	}
}

func TestSplice_AppendsWhenAbsent(t *testing.T) {
	got := Splice("# Theirs\n", BeginMarker+"\nblock\n"+EndMarker)
	if !strings.HasPrefix(got, "# Theirs") {
		t.Errorf("existing content not preserved first:\n%s", got)
	}
	if !strings.Contains(got, "block") {
		t.Errorf("block not appended:\n%s", got)
	}
}

// Splice runs on every up, provision and session start, so repeated
// application is the normal case. It must converge: applying it again
// with the same block is a no-op, never a second block stacked on top
// of (or eating into) the first.
func TestSplice_ConvergesOnRepeatedApplication(t *testing.T) {
	existing := "keep me\n"
	block := BeginMarker + "\nv1\n" + EndMarker

	first := Splice(existing, block)
	second := Splice(first, block)
	third := Splice(second, block)

	if second != first {
		t.Errorf("second application changed the result:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if third != first {
		t.Errorf("third application changed the result:\nfirst:\n%s\nthird:\n%s", first, third)
	}
	if got := strings.Count(third, BeginMarker); got != 1 {
		t.Errorf("BeginMarker count = %d, want 1:\n%s", got, third)
	}
	if got := strings.Count(third, EndMarker); got != 1 {
		t.Errorf("EndMarker count = %d, want 1:\n%s", got, third)
	}
	if !strings.Contains(third, "keep me") {
		t.Errorf("user content lost:\n%s", third)
	}
}

// A begin marker with nothing to close it is the mark of an interrupted
// write -- nothing but cloudlab ever writes a begin marker, and cloudlab
// always writes both together. Everything from that begin marker to EOF
// is our own truncated block, not the user's, so it is safe to discard,
// but content before the marker is the user's and must survive.
func TestSplice_TruncatedBeginWithNoEndIsDiscarded(t *testing.T) {
	existing := "keep me\n\n" + BeginMarker + "\nstray leftover, never closed"
	block := BeginMarker + "\nnew content\n" + EndMarker

	got := Splice(existing, block)

	if !strings.Contains(got, "keep me") {
		t.Errorf("content before the truncated block was lost:\n%s", got)
	}
	if strings.Contains(got, "stray leftover") {
		t.Errorf("truncated block content survived:\n%s", got)
	}
	if got2 := strings.Count(got, BeginMarker); got2 != 1 {
		t.Errorf("BeginMarker count = %d, want 1:\n%s", got2, got)
	}
	if got2 := strings.Count(got, EndMarker); got2 != 1 {
		t.Errorf("EndMarker count = %d, want 1:\n%s", got2, got)
	}
	if !strings.Contains(got, "new content") {
		t.Errorf("new block not written:\n%s", got)
	}
}

// An end marker with no begin marker anywhere in the file is not a
// managed block at all -- just text that happens to contain the string.
// Splice must not treat it as a block to close: it appends, losing
// nothing.
func TestSplice_EndMarkerWithNoBeginIsNotTouched(t *testing.T) {
	existing := "intro\n" + EndMarker + "\noutro\n"
	block := BeginMarker + "\nnew content\n" + EndMarker

	got := Splice(existing, block)

	for _, want := range []string{"intro", "outro", "new content"} {
		if !strings.Contains(got, want) {
			t.Errorf("Splice() lost %q:\n%s", want, got)
		}
	}
	if got2 := strings.Count(got, EndMarker); got2 != 2 {
		t.Errorf("EndMarker count = %d, want 2 (stray original plus the new block's):\n%s", got2, got)
	}
}

// The rendered text is what an agent actually reads, and the seams in
// sessionFacts are fiddly: it is built as raw string plus concatenation
// specifically to embed literal backticks. This pins the Markdown code
// spans so a future edit to that concatenation cannot silently break it.
func TestRender_FactsContainBacktickedCodeSpans(t *testing.T) {
	got, err := Render(nil)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, want := range []string{
		"`pwd`",
		"`git branch --show-current`",
		"`git add -A`",
		"`cloudlab down`",
		"`../TASK.md`",
		"`bd list --status in_progress`",
		"`bd ready`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Render() missing code span %s:\n%s", want, got)
		}
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
