package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/jskswamy/cloudlab/internal/lifecycle"
)

// styles is the small palette the read-only reports (status, list) render
// with. It is built per command rather than kept as a package-level value
// because the decision it encodes -- colour or not -- depends on where the
// command's output is actually going.
type styles struct {
	r      *lipgloss.Renderer
	label  lipgloss.Style
	value  lipgloss.Style
	header lipgloss.Style
	dim    lipgloss.Style
}

// newStyles builds a palette for output going to w.
//
// lipgloss's package-level styles profile os.Stdout, but cobra writes to
// OutOrStdout: in tests that is a bytes.Buffer, and in a pipeline it is a
// pipe, while the process's own stdout may still be a terminal. Profiling
// the actual destination is what keeps escape codes out of piped output
// and out of test assertions.
func newStyles(w io.Writer) styles {
	r := lipgloss.NewRenderer(w)
	return styles{
		r:      r,
		label:  r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "244"}),
		value:  r.NewStyle().Bold(true),
		header: r.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "240", Dark: "247"}),
		dim:    r.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "247", Dark: "243"}),
	}
}

// statusDot renders a provider status with a colour that carries the same
// meaning as the word, for scanning a list without reading it.
func (s styles) statusDot(status string) string {
	colour := lipgloss.Color("214") // amber: in between, or not a state we know
	switch status {
	case "active":
		colour = lipgloss.Color("42")
	case "off", "archive":
		colour = lipgloss.Color("245")
	case "unknown", "":
		colour = lipgloss.Color("203")
	}
	dot := s.r.NewStyle().Foreground(colour).Render("●")
	if status == "" {
		status = "unknown"
	}
	return dot + " " + status
}

// formatUptime renders d at the two coarsest units that say something,
// dropping a zero component: "2d 4h", "3h 12m", "45m".
func formatUptime(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	if h := int(d.Hours()) % 24; h > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}

// formatMoney renders an amount someone is billed, in cents.
func formatMoney(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

// formatRate renders an hourly price, which is a fraction of a cent on
// the smaller sizes and would round to $0.00 at formatMoney's precision.
func formatRate(v float64) string {
	return fmt.Sprintf("$%.4f/hr", v)
}

// costSummary is the one-line answer to "what is this instance costing
// me": what it has accrued, and the rate that produced it.
func costSummary(c lifecycle.Cost) string {
	if !c.Known {
		return "unknown"
	}
	return fmt.Sprintf("%s so far · %s @ %s",
		formatMoney(c.Accrued), formatUptime(c.Uptime), formatRate(c.Hourly))
}

// renderTable lays out rows under headers, sizing each column to its
// widest cell. Cells may already carry styling: widths are measured with
// lipgloss.Width, which ignores escape codes, so a coloured cell lines up
// with a plain one.
//
// footer, when non-nil, is rendered under a rule at the same widths --
// which is how list's cost total stays aligned with the column it totals
// without the caller having to know how wide that column came out.
func renderTable(s styles, headers []string, rows [][]string, footer []string) string {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, row := range append(append([][]string{}, rows...), footerRows(footer)...) {
		for i, cell := range row {
			if i < len(widths) && lipgloss.Width(cell) > widths[i] {
				widths[i] = lipgloss.Width(cell)
			}
		}
	}

	var b strings.Builder
	writeRow := func(cells []string, style lipgloss.Style) {
		b.WriteString("  ")
		for i, cell := range cells {
			if i == len(cells)-1 {
				b.WriteString(style.Render(cell))
				break
			}
			b.WriteString(style.Width(widths[i] + 2).Render(cell))
		}
		b.WriteString("\n")
	}

	writeRow(headers, s.header)
	for _, row := range rows {
		writeRow(row, s.r.NewStyle())
	}
	if footer != nil {
		rule := make([]string, len(footer))
		for i, cell := range footer {
			if cell != "" {
				rule[i] = strings.Repeat("─", widths[i])
			}
		}
		writeRow(rule, s.dim)
		writeRow(footer, s.value)
	}
	return b.String()
}

// footerRows wraps a possibly-nil footer as the zero-or-one extra rows
// that must be measured when sizing columns.
func footerRows(footer []string) [][]string {
	if footer == nil {
		return nil
	}
	return [][]string{footer}
}
