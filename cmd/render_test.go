package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jskswamy/cloudlab/internal/lifecycle"
)

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{45 * time.Minute, "45m"},
		{3*time.Hour + 12*time.Minute, "3h 12m"},
		{50 * time.Hour, "2d 2h"},
		// Whole units read better without a trailing zero component.
		{2 * time.Hour, "2h"},
		{48 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := formatUptime(c.in); got != c.want {
			t.Errorf("formatUptime(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Accrued cost is money and reads in cents; an hourly rate is a fraction
// of a cent and would round to $0.00 at the same precision.
func TestFormatMoneyAndRateUseDifferentPrecision(t *testing.T) {
	if got := formatMoney(0.4213); got != "$0.42" {
		t.Errorf("formatMoney(0.4213) = %q, want %q", got, "$0.42")
	}
	if got := formatRate(0.00893); got != "$0.0089/hr" {
		t.Errorf("formatRate(0.00893) = %q, want %q", got, "$0.0089/hr")
	}
}

func TestCostSummary_ShowsSpentSoFarWithTheRate(t *testing.T) {
	c := lifecycle.Cost{
		Known:   true,
		Uptime:  3*time.Hour + 12*time.Minute,
		Accrued: 0.42,
		Hourly:  0.03571,
		Monthly: 24,
	}

	got := costSummary(c)

	for _, want := range []string{"$0.42", "3h 12m", "$0.0357/hr"} {
		if !strings.Contains(got, want) {
			t.Errorf("costSummary() = %q, want it to contain %q", got, want)
		}
	}
}

// A running instance reported as costing $0.00 is worse than one reported
// as unknown, so an unknown cost must never render as a number.
func TestCostSummary_UnknownNeverRendersAsZero(t *testing.T) {
	got := costSummary(lifecycle.Cost{})

	if !strings.Contains(got, "unknown") {
		t.Errorf("costSummary() = %q, want it to say unknown", got)
	}
	if strings.Contains(got, "$0.00") {
		t.Errorf("costSummary() = %q, want no $0.00 for an unknown cost", got)
	}
}

// lipgloss decides on colour from the writer it is given. Cobra writes to
// OutOrStdout, which in tests and pipes is not a terminal even when the
// process's own stdout is one -- so the renderer must be built from that
// writer, not from os.Stdout, or escape codes leak into piped output.
func TestNewStyles_EmitsNoEscapeCodesForANonTerminalWriter(t *testing.T) {
	var buf bytes.Buffer

	s := newStyles(&buf)
	got := s.label.Render("Provider") + s.value.Render("digitalocean") + s.statusDot("active")

	if strings.Contains(got, "\x1b[") {
		t.Errorf("rendered %q, want no ANSI escape codes for a non-terminal writer", got)
	}
	if !strings.Contains(got, "Provider") || !strings.Contains(got, "digitalocean") {
		t.Errorf("rendered %q, want the plain text preserved", got)
	}
}
