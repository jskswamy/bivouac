package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"github.com/jskswamy/bivouac/internal/provider/gcloud/gcpfake"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/testenv"
)

func writeGCloudConfig(t *testing.T, dir string, externalIP bool, imageRef string) {
	t.Helper()
	path := filepath.Join(dir, "bivouac.pkl")
	key := setupKey(t)
	imageLine := ""
	if imageRef != "" {
		imageLine = fmt.Sprintf("image = %q\n", imageRef)
	}
	body := fmt.Sprintf(`provider = "gcloud"
template = "minimal"
%ssshKeys { %q }
gcloud {
  project = "test-project"
  zone = "us-central1-a"
  machineType = "e2-standard-4"
  diskSizeGb = 50
  externalIP = %t
}
`, imageLine, key.Path, externalIP)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func execCmd(args []string, input string) (string, error) {
	root := newRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "up" {
			root.RemoveCommand(c)
		}
	}
	root.AddCommand(newUpCmdWith(nil, contractSteps()))
	root.SetArgs(args)
	if input != "" {
		root.SetIn(strings.NewReader(input))
	}
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

func TestContractGCloud_Up_Status_List_Down_Lifecycle(t *testing.T) {
	s := gcpfake.NewServer(t)
	defer s.Close()
	s.AddRouter("test-project", "us-central1", &computepb.Router{
		Name: ptr("nat-router"),
		Nats: []*computepb.RouterNat{
			{Name: ptr("nat-gw")},
		},
	})
	t.Setenv("GCLOUD_API_URL", s.URL())
	isolateImageBuildCache(t)
	testenv.Isolate(t)

	dir := initTestRepo(t)
	chdir(t, dir)
	writeGCloudConfig(t, dir, false, "")

	// 1. bivouac up fixture
	out, err := execCmd([]string{"up", "fixture"}, "y\n")
	if err != nil {
		t.Fatalf("up fixture failed: %v\n%s", err, out)
	}

	// Verify instance in gcpfake
	inst, err := s.GetInstance("test-project", "us-central1-a", "fixture")
	if err != nil {
		t.Fatalf("gcpfake.GetInstance: %v", err)
	}
	if inst.GetName() != "fixture" {
		t.Errorf("instance name = %q, want fixture", inst.GetName())
	}
	if !strings.HasSuffix(inst.GetMachineType(), "machineTypes/e2-standard-4") {
		t.Errorf("machineType = %q, want e2-standard-4", inst.GetMachineType())
	}
	if len(inst.GetDisks()) == 0 || inst.GetDisks()[0].GetInitializeParams().GetDiskSizeGb() != 50 {
		t.Errorf("disk size = %+v, want 50GB", inst.GetDisks())
	}

	// Verify no accessConfigs on network interface (externalIP = false)
	if len(inst.GetNetworkInterfaces()) == 0 {
		t.Fatalf("no network interfaces on instance")
	}
	if len(inst.GetNetworkInterfaces()[0].GetAccessConfigs()) != 0 {
		t.Errorf("unexpected accessConfigs when externalIP is false: %+v", inst.GetNetworkInterfaces()[0].GetAccessConfigs())
	}

	// Verify metadata has user-data and ssh-keys
	var hasUserData, hasSSHKeys bool
	for _, item := range inst.GetMetadata().GetItems() {
		if item.GetKey() == "user-data" && item.GetValue() != "" {
			hasUserData = true
		}
		if item.GetKey() == "ssh-keys" && item.GetValue() != "" {
			hasSSHKeys = true
		}
	}
	if !hasUserData {
		t.Error("metadata missing user-data")
	}
	if !hasSSHKeys {
		t.Error("metadata missing ssh-keys")
	}

	// Verify state store
	store, err := state.Open()
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	rec, ok, err := store.Get("fixture")
	if err != nil || !ok {
		t.Fatalf("store.Get(fixture): ok=%v, err=%v", ok, err)
	}
	if rec.Provider != "gcloud" {
		t.Errorf("record provider = %q, want gcloud", rec.Provider)
	}
	if rec.Project != "test-project" {
		t.Errorf("record project = %q, want test-project", rec.Project)
	}
	if rec.Region != "us-central1-a" {
		t.Errorf("record region = %q, want us-central1-a", rec.Region)
	}
	if rec.IP == "" {
		t.Error("record IP is empty")
	}

	// 2. bivouac status fixture --json
	statusOut, err := execCmd([]string{"status", "fixture", "--json"}, "")
	if err != nil {
		t.Fatalf("status fixture --json failed: %v\n%s", err, statusOut)
	}
	var statusResp struct {
		Instance struct {
			Name       string `json:"name"`
			Provider   string `json:"provider"`
			LiveStatus string `json:"liveStatus"`
			IP         string `json:"ip"`
			Region     string `json:"region"`
		} `json:"instance"`
	}
	if err := json.Unmarshal([]byte(statusOut), &statusResp); err != nil {
		t.Fatalf("unmarshal status json: %v\n%s", err, statusOut)
	}
	if statusResp.Instance.LiveStatus != "active" {
		t.Errorf("status = %v, want active", statusResp.Instance.LiveStatus)
	}
	if statusResp.Instance.Provider != "gcloud" {
		t.Errorf("provider = %v, want gcloud", statusResp.Instance.Provider)
	}
	if statusResp.Instance.Region != "us-central1-a" {
		t.Errorf("region = %v, want us-central1-a", statusResp.Instance.Region)
	}

	// 3. bivouac list --json
	listOut, err := execCmd([]string{"list", "--json"}, "")
	if err != nil {
		t.Fatalf("list --json failed: %v\n%s", err, listOut)
	}
	var listResp struct {
		Instances []struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
			IP       string `json:"ip"`
		} `json:"instances"`
	}
	if err := json.Unmarshal([]byte(listOut), &listResp); err != nil {
		t.Fatalf("unmarshal list json: %v\n%s", err, listOut)
	}
	found := false
	for _, item := range listResp.Instances {
		if item.Name == "fixture" {
			found = true
			if item.Provider != "gcloud" {
				t.Errorf("list item provider = %v, want gcloud", item.Provider)
			}
			if item.IP == "" {
				t.Error("list item IP is empty")
			}
			break
		}
	}
	if !found {
		t.Errorf("fixture not found in list output: %s", listOut)
	}

	// 4. bivouac down fixture
	downOut, err := execCmd([]string{"down", "fixture"}, "y\n")
	if err != nil {
		t.Fatalf("down fixture failed: %v\n%s", err, downOut)
	}

	// Verify instance deleted from gcpfake
	if _, err := s.GetInstance("test-project", "us-central1-a", "fixture"); err == nil {
		t.Error("instance still exists on gcpfake after down")
	}

	// Verify removed from state store
	if _, ok, _ := store.Get("fixture"); ok {
		t.Error("fixture record still exists in state store after down")
	}
}

func TestContractGCloud_Up_ExternalIP(t *testing.T) {
	s := gcpfake.NewServer(t)
	defer s.Close()
	t.Setenv("GCLOUD_API_URL", s.URL())
	isolateImageBuildCache(t)
	testenv.Isolate(t)

	dir := initTestRepo(t)
	chdir(t, dir)
	writeGCloudConfig(t, dir, true, "")

	out, err := execCmd([]string{"up", "fixture-ext"}, "y\n")
	if err != nil {
		t.Fatalf("up fixture-ext failed: %v\n%s", err, out)
	}

	inst, err := s.GetInstance("test-project", "us-central1-a", "fixture-ext")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if len(inst.GetNetworkInterfaces()) == 0 {
		t.Fatalf("no network interfaces")
	}
	configs := inst.GetNetworkInterfaces()[0].GetAccessConfigs()
	if len(configs) == 0 || configs[0].GetNatIP() == "" {
		t.Fatalf("expected AccessConfigs with natIP, got %+v", configs)
	}

	store, err := state.Open()
	if err != nil {
		t.Fatal(err)
	}
	rec, ok, _ := store.Get("fixture-ext")
	if !ok || rec.IP != configs[0].GetNatIP() {
		t.Errorf("record IP = %q, want %q", rec.IP, configs[0].GetNatIP())
	}

	_, _ = execCmd([]string{"down", "fixture-ext"}, "y\n")
}

func TestContractGCloud_Up_CustomImageByTag(t *testing.T) {
	s := gcpfake.NewServer(t)
	defer s.Close()
	s.AddRouter("test-project", "us-central1", &computepb.Router{
		Name: ptr("nat-router"),
		Nats: []*computepb.RouterNat{
			{Name: ptr("nat-gw")},
		},
	})
	t.Setenv("GCLOUD_API_URL", s.URL())
	isolateImageBuildCache(t)
	testenv.Isolate(t)

	dir := initTestRepo(t)
	chdir(t, dir)

	now := time.Now().UTC()
	customImageName := "bivouac-custom-img-1"
	s.AddImage("test-project", &computepb.Image{
		Name:              ptr(customImageName),
		Status:            ptr("READY"),
		CreationTimestamp: ptr(now.Format(time.RFC3339)),
		Labels: map[string]string{
			"bivouac-image":  "true",
			"bivouac-format": "1",
			"bivouac-tag":    "my-baked",
			"bivouac-region": "us-central1-a",
			"bivouac-arch":   "x86_64",
			"bivouac-shell":  "fish",
		},
	})

	writeGCloudConfig(t, dir, false, "tag:my-baked")

	out, err := execCmd([]string{"up", "fixture-img"}, "y\n")
	if err != nil {
		t.Fatalf("up fixture-img failed: %v\n%s", err, out)
	}

	inst, err := s.GetInstance("test-project", "us-central1-a", "fixture-img")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	sourceImage := inst.GetDisks()[0].GetInitializeParams().GetSourceImage()
	if !strings.HasSuffix(sourceImage, customImageName) {
		t.Errorf("sourceImage = %q, want suffix %q", sourceImage, customImageName)
	}

	// Verify baked image does not install Nix in user-data
	var userData string
	for _, item := range inst.GetMetadata().GetItems() {
		if item.GetKey() == "user-data" {
			userData = item.GetValue()
		}
	}
	if strings.Contains(userData, nixInstaller) {
		t.Errorf("baked image launch includes Nix installer in user-data:\n%s", userData)
	}

	_, _ = execCmd([]string{"down", "fixture-img"}, "y\n")
}

func TestContractGCloud_Up_MissingCloudNATRejection(t *testing.T) {
	s := gcpfake.NewServer(t)
	defer s.Close()
	t.Setenv("GCLOUD_API_URL", s.URL())
	isolateImageBuildCache(t)
	testenv.Isolate(t)

	dir := initTestRepo(t)
	chdir(t, dir)
	writeGCloudConfig(t, dir, false, "")

	out, err := execCmd([]string{"up", "fixture-nonat"}, "y\n")
	if err == nil {
		t.Fatal("expected up to fail when externalIP is false and Cloud NAT is missing, but it succeeded")
	}
	wantSub := "no Cloud NAT gateway was found in region"
	if !strings.Contains(out, wantSub) {
		t.Errorf("expected output to contain %q, got:\n%s", wantSub, out)
	}
}
