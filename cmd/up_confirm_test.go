package cmd

import (
	"strings"
	"testing"

	"github.com/jskswamy/bivouac/internal/config"
	"github.com/jskswamy/bivouac/internal/keyplan"
	"github.com/jskswamy/bivouac/internal/lifecycle"
	"github.com/jskswamy/bivouac/internal/sshidentity"
	"github.com/jskswamy/bivouac/internal/sshkeys"
)

func TestUpSummary_ShowsApprovedCreation(t *testing.T) {
	plan := lifecycle.UpPlan{Name: "myrepo", ConfigPath: "/repos/myrepo/bivouac.pkl", Config: config.Resolved{Region: "nyc3", Size: "s-2vcpu-4gb", Template: "python"}, Connection: sshidentity.Connection{Alias: "bivouac-myrepo"}, Keys: keyplan.Plan{Entries: []keyplan.Entry{{Key: sshkeys.Key{Path: "/keys/custom.pub"}, AccountName: "bivouac-custom"}, {Key: sshkeys.Key{Path: "/keys/colleague.pub"}, Registered: true, AccountName: "colleague"}}, Identities: []sshidentity.Identity{{PublicPath: "/keys/custom.pub"}}}}
	got := upSummary(plan)
	for _, want := range []string{`"myrepo"`, plan.ConfigPath, "nyc3", "s-2vcpu-4gb", "python", "Proceed?", "will be registered as bivouac-custom", "already registered (colleague)", "extra recipient", "bivouac-myrepo", "managed SSH include"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary lacks %q: %s", want, got)
		}
	}
	if strings.Contains(got, "Image:") {
		t.Fatal("empty image shown")
	}
	plan.Config.Image = "custom-image-123"
	if !strings.Contains(upSummary(plan), "custom-image-123") {
		t.Fatal("image omitted")
	}
}
