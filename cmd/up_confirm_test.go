package cmd

import (
	"strings"
	"testing"

	"github.com/jskswamy/cloudlab/internal/config"
)

func TestUpSummary_ShowsInstanceNameAndConfig(t *testing.T) {
	region, size, template := "nyc3", "s-2vcpu-4gb", "python"
	cfg := config.Config{Region: &region, Size: &size, Template: &template}

	got := upSummary("myrepo", cfg)

	for _, want := range []string{`"myrepo"`, "nyc3", "s-2vcpu-4gb", "python", "Proceed?"} {
		if !strings.Contains(got, want) {
			t.Errorf("upSummary() = %q, want it to contain %q", got, want)
		}
	}
}

func TestUpSummary_OmitsImageLineWhenUnset(t *testing.T) {
	region, size, template := "nyc3", "s-2vcpu-4gb", "python"
	cfg := config.Config{Region: &region, Size: &size, Template: &template}

	got := upSummary("myrepo", cfg)

	if strings.Contains(got, "Image:") {
		t.Errorf("upSummary() = %q, want no Image line when unset", got)
	}
}

func TestUpSummary_IncludesImageWhenSet(t *testing.T) {
	region, size, template := "nyc3", "s-2vcpu-4gb", "python"
	cfg := config.Config{Region: &region, Size: &size, Template: &template, Image: "custom-image-123"}

	got := upSummary("myrepo", cfg)

	if !strings.Contains(got, "custom-image-123") {
		t.Errorf("upSummary() = %q, want it to contain the custom image", got)
	}
}
