package gcloud_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	computepb "cloud.google.com/go/compute/apiv1/computepb"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/gcloud"
	"github.com/jskswamy/bivouac/internal/provider/gcloud/gcpfake"
)

func ptr[T any](v T) *T { return &v }

func TestProvider_Lifecycle(t *testing.T) {
	srv := gcpfake.NewServer(t)
	defer srv.Close()

	srv.AddRouter("test-project", "us-central1", &computepb.Router{
		Name: ptr("nat-router"),
		Nats: []*computepb.RouterNat{
			{Name: ptr("nat-config")},
		},
	})

	p, err := gcloud.New("test-project", "us-central1-a", gcloud.WithBaseURL(srv.URL()))
	if err != nil {
		t.Fatalf("gcloud.New() error = %v", err)
	}

	ctx := context.Background()

	// 1. Create
	spec := provider.InstanceSpec{
		Name:     "test-vm",
		Size:     "e2-standard-4",
		Image:    "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64",
		UserData: "#!/bin/sh\necho hello",
		SSHKeys:  []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG... devuser@host"},
	}
	vm, err := p.Create(ctx, spec)
	if err != nil {
		t.Fatalf("p.Create() error = %v", err)
	}
	if vm.Name != "test-vm" {
		t.Errorf("vm.Name = %q, want test-vm", vm.Name)
	}
	if vm.Region != "us-central1-a" {
		t.Errorf("vm.Region = %q, want us-central1-a", vm.Region)
	}
	if vm.Size != "e2-standard-4" {
		t.Errorf("vm.Size = %q, want e2-standard-4", vm.Size)
	}
	if vm.Status != "active" {
		t.Errorf("vm.Status = %q, want active", vm.Status)
	}
	if vm.IP == "" {
		t.Error("vm.IP is empty")
	}

	// 2. Get
	got, err := p.Get(ctx, vm.ID)
	if err != nil {
		t.Fatalf("p.Get() error = %v", err)
	}
	if got.Name != "test-vm" {
		t.Errorf("got.Name = %q, want test-vm", got.Name)
	}

	// 3. List
	list, err := p.List(ctx)
	if err != nil {
		t.Fatalf("p.List() error = %v", err)
	}
	if len(list) != 1 || list[0].Name != "test-vm" {
		t.Errorf("p.List() = %+v, want 1 VM named test-vm", list)
	}

	// 4. Destroy
	if err := p.Destroy(ctx, vm.ID); err != nil {
		t.Fatalf("p.Destroy() error = %v", err)
	}

	// 5. Get after Destroy
	_, err = p.Get(ctx, vm.ID)
	if err == nil {
		t.Error("p.Get() after destroy expected error, got nil")
	}
}

func TestProvider_ImageManager(t *testing.T) {
	srv := gcpfake.NewServer(t)
	defer srv.Close()

	srv.AddImage("test-project", &computepb.Image{
		Name:             ptr("bivouac-mydev-20261023"),
		Status:           ptr("READY"),
		DiskSizeGb:       ptr(int64(50)),
		LabelFingerprint: ptr("fp1"),
		Labels: map[string]string{
			"bivouac-image":  "true",
			"bivouac-format": "1",
			"bivouac-arch":   "x86_64",
			"bivouac-region": "us-central1-a",
			"bivouac-shell":  "bash",
			"bivouac-tag":    "mydev",
		},
	})

	p, err := gcloud.New("test-project", "us-central1-a",
		gcloud.WithBaseURL(srv.URL()),
		gcloud.WithDiskSizeGb(60),
	)
	if err != nil {
		t.Fatalf("gcloud.New() error = %v", err)
	}

	im, ok := any(p).(provider.ImageManager)
	if !ok {
		t.Fatal("provider does not implement provider.ImageManager")
	}

	ctx := context.Background()

	// 1. ListImages
	imgs, err := im.ListImages(ctx, "")
	if err != nil {
		t.Fatalf("im.ListImages() error = %v", err)
	}
	if len(imgs) != 1 {
		t.Fatalf("im.ListImages() returned %d images, want 1", len(imgs))
	}
	if imgs[0].Name != "bivouac-mydev-20261023" {
		t.Errorf("image Name = %q, want bivouac-mydev-20261023", imgs[0].Name)
	}
	if imgs[0].Status != "available" {
		t.Errorf("image Status = %q, want available", imgs[0].Status)
	}
	if !slices.Contains(imgs[0].Tags, "mydev") || !slices.Contains(imgs[0].Tags, "bivouac-image") {
		t.Errorf("unexpected tags: %v", imgs[0].Tags)
	}

	// 2. ListImages with matching tag
	tagged, err := im.ListImages(ctx, "mydev")
	if err != nil {
		t.Fatalf("im.ListImages(mydev) error = %v", err)
	}
	if len(tagged) != 1 {
		t.Errorf("im.ListImages(mydev) returned %d images, want 1", len(tagged))
	}

	// 3. ListImages with non-matching tag
	none, err := im.ListImages(ctx, "other")
	if err != nil {
		t.Fatalf("im.ListImages(other) error = %v", err)
	}
	if len(none) != 0 {
		t.Errorf("im.ListImages(other) returned %d images, want 0", len(none))
	}

	// 4. GetImage
	got, err := im.GetImage(ctx, "bivouac-mydev-20261023")
	if err != nil {
		t.Fatalf("im.GetImage() error = %v", err)
	}
	if got.Name != "bivouac-mydev-20261023" {
		t.Errorf("got Name = %q, want bivouac-mydev-20261023", got.Name)
	}

	// 5. GetImage not found
	_, err = im.GetImage(ctx, "nonexistent")
	if !errors.Is(err, provider.ErrImageNotFound) {
		t.Errorf("im.GetImage(nonexistent) error = %v, want ErrImageNotFound", err)
	}

	// 6. TagImage
	if err := im.TagImage(ctx, "bivouac-mydev-20261023", "newtag"); err != nil {
		t.Fatalf("im.TagImage() error = %v", err)
	}
	retagged, err := im.GetImage(ctx, "bivouac-mydev-20261023")
	if err != nil {
		t.Fatalf("im.GetImage() after tagging error = %v", err)
	}
	if !slices.Contains(retagged.Tags, "newtag") {
		t.Errorf("tags after tagging = %v, want newtag", retagged.Tags)
	}

	// 7. SizeDiskGB
	disk, err := im.SizeDiskGB(ctx, "e2-standard-4")
	if err != nil {
		t.Fatalf("im.SizeDiskGB() error = %v", err)
	}
	if disk != 60 {
		t.Errorf("im.SizeDiskGB() = %d, want 60", disk)
	}

	// 8. DeleteImage
	if err := im.DeleteImage(ctx, "bivouac-mydev-20261023"); err != nil {
		t.Fatalf("im.DeleteImage() error = %v", err)
	}

	// 9. Get after DeleteImage
	_, err = im.GetImage(ctx, "bivouac-mydev-20261023")
	if !errors.Is(err, provider.ErrImageNotFound) {
		t.Errorf("im.GetImage() after delete error = %v, want ErrImageNotFound", err)
	}

	// 10. DeleteImage after DeleteImage
	err = im.DeleteImage(ctx, "bivouac-mydev-20261023")
	if !errors.Is(err, provider.ErrImageNotFound) {
		t.Errorf("im.DeleteImage() again error = %v, want ErrImageNotFound", err)
	}
}

func TestProvider_CloudNATPreflight(t *testing.T) {
	srv := gcpfake.NewServer(t)
	defer srv.Close()

	ctx := context.Background()
	spec := provider.InstanceSpec{
		Name:  "test-vm",
		Size:  "e2-standard-4",
		Image: "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64",
	}

	t.Run("fails when externalIP is false and no Cloud NAT exists", func(t *testing.T) {
		p, err := gcloud.New("test-project", "us-central1-a",
			gcloud.WithBaseURL(srv.URL()),
			gcloud.WithExternalIP(false),
		)
		if err != nil {
			t.Fatalf("gcloud.New() error = %v", err)
		}
		defer func() { _ = p.Close() }()

		_, err = p.Create(ctx, spec)
		if err == nil {
			t.Fatal("expected error when externalIP=false and no Cloud NAT exists, got nil")
		}
		wantSub := "no Cloud NAT gateway was found"
		if !strings.Contains(err.Error(), wantSub) {
			t.Errorf("p.Create() error = %q, want substring %q", err.Error(), wantSub)
		}
	})

	t.Run("succeeds when externalIP is false and Cloud NAT exists", func(t *testing.T) {
		srv.AddRouter("test-project", "us-central1", &computepb.Router{
			Name: ptr("nat-router"),
			Nats: []*computepb.RouterNat{
				{Name: ptr("nat-gw")},
			},
		})

		p, err := gcloud.New("test-project", "us-central1-a",
			gcloud.WithBaseURL(srv.URL()),
			gcloud.WithExternalIP(false),
		)
		if err != nil {
			t.Fatalf("gcloud.New() error = %v", err)
		}
		defer func() { _ = p.Close() }()

		vm, err := p.Create(ctx, spec)
		if err != nil {
			t.Fatalf("p.Create() error = %v", err)
		}
		if vm.Name != "test-vm" {
			t.Errorf("vm.Name = %q, want test-vm", vm.Name)
		}
	})

	t.Run("succeeds when externalIP is true without Cloud NAT", func(t *testing.T) {
		// Create a separate server with no routers
		cleanSrv := gcpfake.NewServer(t)
		defer cleanSrv.Close()

		p, err := gcloud.New("test-project", "us-central1-a",
			gcloud.WithBaseURL(cleanSrv.URL()),
			gcloud.WithExternalIP(true),
		)
		if err != nil {
			t.Fatalf("gcloud.New() error = %v", err)
		}
		defer func() { _ = p.Close() }()

		vm, err := p.Create(ctx, spec)
		if err != nil {
			t.Fatalf("p.Create() error = %v", err)
		}
		if vm.Name != "test-vm" {
			t.Errorf("vm.Name = %q, want test-vm", vm.Name)
		}
	})
}
