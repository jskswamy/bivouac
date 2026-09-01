package digitalocean

import (
	"context"
	"net/http"
	"testing"
)

func TestListRegions(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/regions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"regions":[{"slug":"nyc3","name":"New York 3","sizes":["s-1vcpu-1gb"],"available":true}],"links":{}}`))
	})

	regions, err := p.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions() error = %v", err)
	}
	if len(regions) != 1 {
		t.Fatalf("ListRegions() returned %d regions, want 1", len(regions))
	}
	got := regions[0]
	if got.Slug != "nyc3" || got.Name != "New York 3" || !got.Available {
		t.Errorf("ListRegions()[0] = %+v, want slug=nyc3 name=\"New York 3\" available=true", got)
	}
	if len(got.Sizes) != 1 || got.Sizes[0] != "s-1vcpu-1gb" {
		t.Errorf("ListRegions()[0].Sizes = %v, want [s-1vcpu-1gb]", got.Sizes)
	}
}

func TestListRegions_MultiplePages(t *testing.T) {
	p, mux := newTestProvider(t)
	requests := 0
	mux.HandleFunc("/v2/regions", func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(`{"regions":[{"slug":"sfo3","name":"San Francisco 3","sizes":["s-1vcpu-1gb"],"available":true}],"links":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"regions":[{"slug":"nyc3","name":"New York 3","sizes":["s-1vcpu-1gb"],"available":true}],"links":{"pages":{"next":"` + r.Host + `/v2/regions/?page=2"}}}`))
	})

	regions, err := p.ListRegions(context.Background())
	if err != nil {
		t.Fatalf("ListRegions() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("handler called %d times, want 2 (one per page)", requests)
	}
	if len(regions) != 2 {
		t.Fatalf("ListRegions() returned %d regions across 2 pages, want 2", len(regions))
	}
	if regions[0].Slug != "nyc3" || regions[1].Slug != "sfo3" {
		t.Errorf("ListRegions() = %+v, want slugs nyc3 and sfo3", regions)
	}
}

func TestListSizes_MarksGPUSizes(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/sizes", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"sizes":[
			{"slug":"s-1vcpu-1gb","memory":1024,"vcpus":1,"disk":25,"price_monthly":6,"regions":["nyc3"],"available":true,"description":"Basic"},
			{"slug":"gpu-h100x1-80gb","memory":245760,"vcpus":20,"disk":720,"price_monthly":2699.2,"regions":["nyc2"],"available":true,"description":"GPU","gpu_info":{"count":1,"model":"h100"}}
		],"links":{}}`))
	})

	sizes, err := p.ListSizes(context.Background())
	if err != nil {
		t.Fatalf("ListSizes() error = %v", err)
	}
	if len(sizes) != 2 {
		t.Fatalf("ListSizes() returned %d sizes, want 2", len(sizes))
	}

	cpu, gpu := sizes[0], sizes[1]
	if cpu.GPU {
		t.Errorf("cpu size %q: GPU = true, want false", cpu.Slug)
	}
	if !gpu.GPU || gpu.GPUModel != "h100" {
		t.Errorf("gpu size %q: GPU=%v GPUModel=%q, want GPU=true GPUModel=h100", gpu.Slug, gpu.GPU, gpu.GPUModel)
	}
	if gpu.VCPUs != 20 || gpu.MemoryMB != 245760 || gpu.DiskGB != 720 {
		t.Errorf("gpu size = %+v, want VCPUs=20 MemoryMB=245760 DiskGB=720", gpu)
	}
}

func TestListImages_UsesDistributionEndpoint(t *testing.T) {
	p, mux := newTestProvider(t)
	mux.HandleFunc("/v2/images", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "distribution" {
			t.Errorf("query type = %q, want distribution", r.URL.Query().Get("type"))
		}
		_, _ = w.Write([]byte(`{"images":[{"slug":"ubuntu-22-04-x64","name":"Ubuntu 22.04","distribution":"Ubuntu","public":true,"regions":["nyc3"]}],"links":{}}`))
	})

	images, err := p.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages() error = %v", err)
	}
	if len(images) != 1 {
		t.Fatalf("ListImages() returned %d images, want 1", len(images))
	}
	got := images[0]
	if got.Slug != "ubuntu-22-04-x64" || got.Distribution != "Ubuntu" || !got.Public {
		t.Errorf("ListImages()[0] = %+v, want slug=ubuntu-22-04-x64 distribution=Ubuntu public=true", got)
	}
}
