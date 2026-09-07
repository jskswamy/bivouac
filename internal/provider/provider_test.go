package provider

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestReportProgress_CallsAttachedFunc(t *testing.T) {
	var got []string
	ctx := WithProgress(context.Background(), func(status string) {
		got = append(got, status)
	})

	ReportProgress(ctx, "first")
	ReportProgress(ctx, "second")

	want := []string{"first", "second"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestReportProgress_NoopWithoutProgressFunc(t *testing.T) {
	// Must not panic when no ProgressFunc was attached via WithProgress.
	ReportProgress(context.Background(), "ignored")
}

func TestOutput_ReturnsAttachedWriters(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := WithOutput(context.Background(), &out, &errOut)

	gotOut, gotErrOut := Output(ctx)
	if gotOut != io.Writer(&out) {
		t.Errorf("Output() stdout = %v, want the attached writer", gotOut)
	}
	if gotErrOut != io.Writer(&errOut) {
		t.Errorf("Output() stderr = %v, want the attached writer", gotErrOut)
	}
}

func TestOutput_DefaultsToOsStdoutStderrWithoutWithOutput(t *testing.T) {
	gotOut, gotErrOut := Output(context.Background())
	if gotOut != io.Writer(os.Stdout) {
		t.Errorf("Output() stdout = %v, want os.Stdout", gotOut)
	}
	if gotErrOut != io.Writer(os.Stderr) {
		t.Errorf("Output() stderr = %v, want os.Stderr", gotErrOut)
	}
}

func TestReportWarning_WritesToTheErrorWriter(t *testing.T) {
	var out, errOut bytes.Buffer
	ctx := WithOutput(context.Background(), &out, &errOut)

	ReportWarning(ctx, "beads: could not seed issues")

	if got := errOut.String(); !strings.Contains(got, "beads: could not seed issues") {
		t.Errorf("errOut = %q, want it to contain the warning", got)
	}
	if got := errOut.String(); !strings.HasPrefix(got, "warning: ") {
		t.Errorf("errOut = %q, want a \"warning: \" prefix", got)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing — a warning is not progress", out.String())
	}
}
