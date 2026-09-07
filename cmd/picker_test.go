package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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
