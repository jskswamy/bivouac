package provider

import (
	"bytes"
	"context"
	"io"
	"os"
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
