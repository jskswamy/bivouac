package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
)

// A picker that prompts where nothing can answer is a hang. CI, a script and
// an agent driving cloudlab all get the error instead.
func TestPickSession_ReadsAChoiceFromStdin(t *testing.T) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(strings.NewReader("2\n"))

	got, err := pickSession(c, []string{"auth", "docs"})
	if err != nil {
		t.Fatalf("pickSession() error = %v", err)
	}
	if got != "docs" {
		t.Errorf("pickSession() = %q, want docs", got)
	}
	if !strings.Contains(out.String(), "auth") || !strings.Contains(out.String(), "docs") {
		t.Errorf("prompt did not list both candidates: %s", out.String())
	}
}

func TestPickSession_RejectsOutOfRangeAndGarbage(t *testing.T) {
	for _, in := range []string{"0\n", "3\n", "banana\n", "\n"} {
		c := &cobra.Command{}
		c.SetOut(&bytes.Buffer{})
		c.SetIn(strings.NewReader(in))
		if _, err := pickSession(c, []string{"auth", "docs"}); err == nil {
			t.Errorf("pickSession(%q) = nil, want an error", in)
		}
	}
}

// pickListener and pickServeEntry had no tests at all, so the columns they
// print -- the part that differs between the three pickers, and the only
// part a shared implementation could get wrong -- were unpinned.
func TestPickListener_ListsPortProcessAndAddressThenReadsAChoice(t *testing.T) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(strings.NewReader("2\n"))

	got, err := pickListener(c, []lifecycle.Listener{
		{Port: 8080, Process: "vite", Addr: "127.0.0.1"},
		{Port: 5432, Addr: "0.0.0.0"},
	})
	if err != nil {
		t.Fatalf("pickListener() error = %v", err)
	}
	if got.Port != 5432 {
		t.Errorf("pickListener() = %+v, want the second entry", got)
	}

	printed := out.String()
	for _, want := range []string{
		"Listening on the instance:",
		"  1) 8080   vite         127.0.0.1",
		// A listener with no process name renders as "-", not as a blank
		// column that silently shifts the address left.
		"  2) 5432   -            0.0.0.0",
		"Which one? ",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("prompt missing %q, got:\n%s", want, printed)
		}
	}
}

func TestPickServeEntry_ListsPortAndForwardThenReadsAChoice(t *testing.T) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(strings.NewReader("1\n"))

	got, err := pickServeEntry(c, []lifecycle.ServeEntry{
		{Port: 3000, Forward: "http://example.ts.net:3000"},
		{Port: 9000, Forward: "http://example.ts.net:9000"},
	})
	if err != nil {
		t.Fatalf("pickServeEntry() error = %v", err)
	}
	if got.Port != 3000 {
		t.Errorf("pickServeEntry() = %+v, want the first entry", got)
	}

	printed := out.String()
	for _, want := range []string{
		"Serving on the instance:",
		"  1) 3000   http://example.ts.net:3000",
		"  2) 9000   http://example.ts.net:9000",
		"Which one? ",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("prompt missing %q, got:\n%s", want, printed)
		}
	}
}

// Every picker refuses a choice outside the range it offered, rather than
// indexing past the end of its own list.
func TestPickers_RejectAnOutOfRangeChoice(t *testing.T) {
	c := &cobra.Command{}
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetIn(strings.NewReader("3\n"))
	if _, err := pickListener(c, []lifecycle.Listener{{Port: 1}, {Port: 2}}); err == nil {
		t.Error("pickListener() error = nil, want a range error")
	}

	c2 := &cobra.Command{}
	c2.SetOut(&bytes.Buffer{})
	c2.SetIn(strings.NewReader("0\n"))
	if _, err := pickServeEntry(c2, []lifecycle.ServeEntry{{Port: 1}}); err == nil {
		t.Error("pickServeEntry() error = nil, want a range error")
	}
}
