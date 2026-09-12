package cmd

import (
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func TestUpSummary_ShowsInstanceNameAndConfig(t *testing.T) {
	cfg := config.Resolved{Region: "nyc3", Size: "s-2vcpu-4gb", Template: "python"}

	got := upSummary("myrepo", cfg)

	for _, want := range []string{`"myrepo"`, "nyc3", "s-2vcpu-4gb", "python", "Proceed?"} {
		if !strings.Contains(got, want) {
			t.Errorf("upSummary() = %q, want it to contain %q", got, want)
		}
	}
}

func TestUpSummary_OmitsImageLineWhenUnset(t *testing.T) {
	cfg := config.Resolved{Region: "nyc3", Size: "s-2vcpu-4gb", Template: "python"}

	got := upSummary("myrepo", cfg)

	if strings.Contains(got, "Image:") {
		t.Errorf("upSummary() = %q, want no Image line when unset", got)
	}
}

func TestUpSummary_IncludesImageWhenSet(t *testing.T) {
	cfg := config.Resolved{
		Region: "nyc3", Size: "s-2vcpu-4gb", Template: "python",
		Config: config.Config{Image: "custom-image-123"},
	}

	got := upSummary("myrepo", cfg)

	if !strings.Contains(got, "custom-image-123") {
		t.Errorf("upSummary() = %q, want it to contain the custom image", got)
	}
}
