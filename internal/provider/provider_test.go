package provider

import (
	"context"
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
