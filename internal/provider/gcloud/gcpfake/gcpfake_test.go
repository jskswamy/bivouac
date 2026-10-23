package gcpfake_test

import (
	"context"
	"testing"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"

	"github.com/jskswamy/bivouac/internal/provider/gcloud/gcpfake"
)

func TestServer_CreateAndGetInstance(t *testing.T) {
	srv := gcpfake.NewServer(t)
	defer srv.Close()

	ctx := context.Background()
	client, err := compute.NewInstancesRESTClient(ctx, srv.ClientOptions()...)
	if err != nil {
		t.Fatalf("NewInstancesRESTClient() err = %v", err)
	}
	defer func() { _ = client.Close() }()

	op, err := client.Insert(ctx, &computepb.InsertInstanceRequest{
		Project: "test-proj",
		Zone:    "us-central1-a",
		InstanceResource: &computepb.Instance{
			Name:        ptr("test-inst"),
			MachineType: ptr("zones/us-central1-a/machineTypes/e2-standard-4"),
		},
	})
	if err != nil {
		t.Fatalf("Insert() err = %v", err)
	}
	if err := op.Wait(ctx); err != nil {
		t.Fatalf("op.Wait() err = %v", err)
	}

	inst, err := client.Get(ctx, &computepb.GetInstanceRequest{
		Project:  "test-proj",
		Zone:     "us-central1-a",
		Instance: "test-inst",
	})
	if err != nil {
		t.Fatalf("Get() err = %v", err)
	}
	if inst.GetName() != "test-inst" {
		t.Errorf("inst.GetName() = %q, want test-inst", inst.GetName())
	}
	if inst.GetStatus() != "RUNNING" {
		t.Errorf("inst.GetStatus() = %q, want RUNNING", inst.GetStatus())
	}

	// Test List
	it := client.List(ctx, &computepb.ListInstancesRequest{
		Project: "test-proj",
		Zone:    "us-central1-a",
	})
	listed, err := it.Next()
	if err != nil {
		t.Fatalf("List() err = %v", err)
	}
	if listed.GetName() != "test-inst" {
		t.Errorf("listed.GetName() = %q, want test-inst", listed.GetName())
	}

	// Test Delete
	delOp, err := client.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  "test-proj",
		Zone:     "us-central1-a",
		Instance: "test-inst",
	})
	if err != nil {
		t.Fatalf("Delete() err = %v", err)
	}
	if err := delOp.Wait(ctx); err != nil {
		t.Fatalf("delOp.Wait() err = %v", err)
	}

	// Verify not found after delete
	_, err = client.Get(ctx, &computepb.GetInstanceRequest{
		Project:  "test-proj",
		Zone:     "us-central1-a",
		Instance: "test-inst",
	})
	if err == nil {
		t.Fatal("Get() after Delete expected error, got nil")
	}
}

func TestServer_Images(t *testing.T) {
	srv := gcpfake.NewServer(t)
	defer srv.Close()

	ctx := context.Background()
	client, err := compute.NewImagesRESTClient(ctx, srv.ClientOptions()...)
	if err != nil {
		t.Fatalf("NewImagesRESTClient() err = %v", err)
	}
	defer func() { _ = client.Close() }()

	op, err := client.Insert(ctx, &computepb.InsertImageRequest{
		Project: "test-proj",
		ImageResource: &computepb.Image{
			Name: ptr("bivouac-ubuntu-base"),
		},
	})
	if err != nil {
		t.Fatalf("InsertImage() err = %v", err)
	}
	if err := op.Wait(ctx); err != nil {
		t.Fatalf("op.Wait() err = %v", err)
	}

	img, err := client.Get(ctx, &computepb.GetImageRequest{
		Project: "test-proj",
		Image:   "bivouac-ubuntu-base",
	})
	if err != nil {
		t.Fatalf("GetImage() err = %v", err)
	}
	if img.GetName() != "bivouac-ubuntu-base" {
		t.Errorf("img.GetName() = %q, want bivouac-ubuntu-base", img.GetName())
	}
}

func ptr[T any](v T) *T { return &v }
