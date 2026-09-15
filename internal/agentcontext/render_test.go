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

// countOnOwnLines counts only the marker occurrences Splice acts on: the
// ones alone on a line. A marker quoted inside a sentence is user text,
// so a plain strings.Count would conflate the two and pass against the
// bug these tests pin.
func countOnOwnLines(s, marker string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if line == marker {
			n++
		}
	}
	return n
}

// cloudlab's own documentation quotes both markers in order, so a user's
// instructions file can hold the pair as prose while holding no managed
// block at all. A substring search finds the quoted end marker, cuts the
// file there and re-appends the tail alongside a fresh block -- on every
// up, every provision and every session start, into a file an agent
// loads each session.
func TestSplice_IgnoresMarkersQuotedInUserProse(t *testing.T) {
	existing := "# Notes\n\n" +
		"The block is delimited by `" + BeginMarker + "` and `" + EndMarker + "`.\n" +
		"Everything between them belongs to cloudlab.\n"
	block := BeginMarker + "\nnew content\n" + EndMarker

	got := Splice(existing, block)

	if !strings.HasPrefix(got, existing) {
		t.Errorf("user content was not left alone:\nwant prefix:\n%s\ngot:\n%s", existing, got)
	}
	if !strings.Contains(got, "new content") {
		t.Errorf("block not appended:\n%s", got)
	}
	if n := countOnOwnLines(got, BeginMarker); n != 1 {
		t.Errorf("begin markers on their own line = %d, want 1:\n%s", n, got)
	}
	if n := countOnOwnLines(got, EndMarker); n != 1 {
		t.Errorf("end markers on their own line = %d, want 1:\n%s", n, got)
	}
}

// A file cloudlab has already written, whose user half quotes both
// markers above the managed block, must still converge on exactly one
// pair. A substring search cuts at the quoted markers instead, burying
// the new block inside the user's sentence and leaving the real block
// below it untouched -- so the user's prose is destroyed and cloudlab's
// own facts silently stop updating on every later write.
func TestSplice_ConvergesWhenUserProseQuotesTheMarkers(t *testing.T) {
	prose := "# Notes\n\nSee `" + BeginMarker + "` and `" + EndMarker + "` in the docs.\n"
	existing := prose + "\n" + BeginMarker + "\nv0\n" + EndMarker + "\n"
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
	if n := countOnOwnLines(third, BeginMarker); n != 1 {
		t.Errorf("begin markers on their own line = %d, want 1:\n%s", n, third)
	}
	if n := countOnOwnLines(third, EndMarker); n != 1 {
		t.Errorf("end markers on their own line = %d, want 1:\n%s", n, third)
	}
	if !strings.HasPrefix(third, prose) {
		t.Errorf("user prose quoting the markers was lost:\n%s", third)
	}
	if strings.Contains(third, "v0") {
		t.Errorf("the previous block survived:\n%s", third)
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
