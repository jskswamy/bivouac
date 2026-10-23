package gcloud_test

import (
	"context"
	"testing"

	"github.com/jskswamy/bivouac/internal/provider"
	"github.com/jskswamy/bivouac/internal/provider/gcloud"
	"github.com/jskswamy/bivouac/internal/provider/gcloud/gcpfake"
)

func TestProvider_Lifecycle(t *testing.T) {
	srv := gcpfake.NewServer(t)
	defer srv.Close()

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
