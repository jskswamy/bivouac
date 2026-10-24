package gcloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	compute "cloud.google.com/go/compute/apiv1"
	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"golang.org/x/crypto/ssh"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/jskswamy/bivouac/internal/provider"
)

var _ provider.Provider = (*Provider)(nil)
var _ provider.KeyRegistry = (*Provider)(nil)

// Provider manages VM lifecycles on Google Compute Engine.
type Provider struct {
	project         string
	zone            string
	instancesClient *compute.InstancesClient
	imagesClient    *compute.ImagesClient
	routersClient   *compute.RoutersClient
	diskSizeGb      int
	externalIP      bool
}

// New creates and configures a new Google Cloud provider for project and zone.
func New(project, zone string, opts ...Option) (*Provider, error) {
	if project == "" {
		return nil, fmt.Errorf("gcloud project is required")
	}
	if zone == "" {
		return nil, fmt.Errorf("gcloud zone is required")
	}

	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	var clientOpts []option.ClientOption
	if cfg.baseURL != "" {
		clientOpts = append(clientOpts, option.WithEndpoint(cfg.baseURL), option.WithoutAuthentication())
	} else if len(cfg.credentialsJSON) > 0 {
		clientOpts = append(clientOpts, option.WithAuthCredentialsJSON(option.ServiceAccount, cfg.credentialsJSON))
	} else if cfg.credentialsFile != "" {
		clientOpts = append(clientOpts, option.WithAuthCredentialsFile(option.ServiceAccount, cfg.credentialsFile))
	}

	if cfg.httpClient != nil {
		clientOpts = append(clientOpts, option.WithHTTPClient(cfg.httpClient))
	}
	clientOpts = append(clientOpts, cfg.extraClientOpts...)

	ctx := context.Background()
	instancesClient, err := compute.NewInstancesRESTClient(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("creating gce instances client: %w", err)
	}
	imagesClient, err := compute.NewImagesRESTClient(ctx, clientOpts...)
	if err != nil {
		_ = instancesClient.Close()
		return nil, fmt.Errorf("creating gce images client: %w", err)
	}
	routersClient, err := compute.NewRoutersRESTClient(ctx, clientOpts...)
	if err != nil {
		_ = instancesClient.Close()
		_ = imagesClient.Close()
		return nil, fmt.Errorf("creating gce routers client: %w", err)
	}

	return &Provider{
		project:         project,
		zone:            zone,
		instancesClient: instancesClient,
		imagesClient:    imagesClient,
		routersClient:   routersClient,
		diskSizeGb:      cfg.diskSizeGb,
		externalIP:      cfg.externalIP,
	}, nil
}

// Close closes any underlying clients.
func (p *Provider) Close() error {
	var errs []error
	if p.instancesClient != nil {
		if err := p.instancesClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if p.imagesClient != nil {
		if err := p.imagesClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if p.routersClient != nil {
		if err := p.routersClient.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Create provisions a new GCE VM instance.
func (p *Provider) Create(ctx context.Context, spec provider.InstanceSpec) (provider.VM, error) {
	if !p.externalIP {
		region := regionFromZone(p.zone)
		hasNAT, err := p.hasCloudNAT(ctx, region)
		if err != nil {
			return provider.VM{}, fmt.Errorf("checking Cloud NAT in region %s: %w", region, err)
		}
		if !hasNAT {
			return provider.VM{}, fmt.Errorf("externalIP is false and no Cloud NAT gateway was found in region %q. VM instances without an external IP or Cloud NAT cannot reach the internet to provision (Nix installation). Either set gcloud.externalIP: true or create a Cloud NAT in %s (see docs/gcloud.md)", region, region)
		}
	}

	machineType := spec.Size
	if !strings.Contains(machineType, "/") {
		machineType = fmt.Sprintf("zones/%s/machineTypes/%s", p.zone, spec.Size)
	}

	disk := &computepb.AttachedDisk{
		Boot:       ptr(true),
		AutoDelete: ptr(true),
		InitializeParams: &computepb.AttachedDiskInitializeParams{
			SourceImage: ptr(spec.Image),
		},
	}
	if p.diskSizeGb > 0 {
		disk.InitializeParams.DiskSizeGb = ptr(int64(p.diskSizeGb))
	}

	nic := &computepb.NetworkInterface{
		Network: ptr("global/networks/default"),
	}
	if p.externalIP {
		nic.AccessConfigs = []*computepb.AccessConfig{
			{
				Name: ptr("External NAT"),
				Type: ptr("ONE_TO_ONE_NAT"),
			},
		}
	}

	var metadataItems []*computepb.Items
	if spec.UserData != "" {
		metadataItems = append(metadataItems, &computepb.Items{
			Key:   ptr("user-data"),
			Value: ptr(spec.UserData),
		})
	}
	if len(spec.SSHKeys) > 0 {
		var keyLines []string
		for _, key := range spec.SSHKeys {
			if strings.Contains(key, ":") {
				keyLines = append(keyLines, key)
				continue
			}
			if spec.User != "" {
				keyLines = append(keyLines, spec.User+":"+key)
			}
			keyLines = append(keyLines, "root:"+key)
		}
		metadataItems = append(metadataItems, &computepb.Items{
			Key:   ptr("ssh-keys"),
			Value: ptr(strings.Join(keyLines, "\n")),
		})
	}

	instResource := &computepb.Instance{
		Name:              ptr(spec.Name),
		MachineType:       ptr(machineType),
		Disks:             []*computepb.AttachedDisk{disk},
		NetworkInterfaces: []*computepb.NetworkInterface{nic},
		Metadata: &computepb.Metadata{
			Items: metadataItems,
		},
	}

	op, err := p.instancesClient.Insert(ctx, &computepb.InsertInstanceRequest{
		Project:          p.project,
		Zone:             p.zone,
		InstanceResource: instResource,
	})
	if err != nil {
		return provider.VM{}, fmt.Errorf("creating gce instance %s: %w", spec.Name, err)
	}

	if err := op.Wait(ctx); err != nil {
		return provider.VM{}, fmt.Errorf("waiting for gce instance %s to be created: %w", spec.Name, err)
	}

	return p.Get(ctx, spec.Name)
}

// Destroy deletes an instance by name or ID.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	op, err := p.instancesClient.Delete(ctx, &computepb.DeleteInstanceRequest{
		Project:  p.project,
		Zone:     p.zone,
		Instance: id,
	})
	if err != nil {
		return fmt.Errorf("destroying gce instance %s: %w", id, err)
	}

	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("waiting for gce instance %s deletion: %w", id, err)
	}
	return nil
}

// Get fetches details of a single instance.
func (p *Provider) Get(ctx context.Context, id string) (provider.VM, error) {
	inst, err := p.instancesClient.Get(ctx, &computepb.GetInstanceRequest{
		Project:  p.project,
		Zone:     p.zone,
		Instance: id,
	})
	if err != nil {
		return provider.VM{}, fmt.Errorf("getting gce instance %s: %w", id, err)
	}
	return p.toVM(inst), nil
}

// List enumerates instances in the configured project and zone.
func (p *Provider) List(ctx context.Context) ([]provider.VM, error) {
	it := p.instancesClient.List(ctx, &computepb.ListInstancesRequest{
		Project: p.project,
		Zone:    p.zone,
	})

	var vms []provider.VM
	for {
		inst, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("listing gce instances: %w", err)
		}
		vms = append(vms, p.toVM(inst))
	}
	return vms, nil
}

// ListKeys satisfies provider.KeyRegistry. For Google Cloud, keys are injected
// directly into instance metadata at creation rather than account-wide registry.
func (p *Provider) ListKeys(ctx context.Context) ([]provider.SSHKey, error) {
	return nil, nil
}

// CreateKey satisfies provider.KeyRegistry.
func (p *Provider) CreateKey(ctx context.Context, name, publicKey string) (provider.SSHKey, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	fp := ""
	if err == nil {
		fp = ssh.FingerprintLegacyMD5(pub)
	}
	return provider.SSHKey{
		ID:          publicKey,
		Name:        name,
		PublicKey:   publicKey,
		Fingerprint: fp,
	}, nil
}

func (p *Provider) toVM(inst *computepb.Instance) provider.VM {
	ip := ""
	for _, nic := range inst.NetworkInterfaces {
		for _, ac := range nic.AccessConfigs {
			if ac.NatIP != nil && *ac.NatIP != "" {
				ip = *ac.NatIP
				break
			}
		}
		if ip == "" && nic.NetworkIP != nil {
			ip = *nic.NetworkIP
		}
		if ip != "" {
			break
		}
	}

	status := "off"
	if inst.GetStatus() == "RUNNING" {
		status = "active"
	} else if inst.GetStatus() == "PROVISIONING" || inst.GetStatus() == "STAGING" {
		status = "provisioning"
	}

	machineType := inst.GetMachineType()
	if idx := strings.LastIndex(machineType, "/"); idx >= 0 {
		machineType = machineType[idx+1:]
	}

	var createdAt time.Time
	if inst.CreationTimestamp != nil {
		t, _ := time.Parse(time.RFC3339, *inst.CreationTimestamp)
		createdAt = t
	}

	return provider.VM{
		ID:        inst.GetName(),
		Name:      inst.GetName(),
		IP:        ip,
		Region:    p.zone,
		Size:      machineType,
		Status:    status,
		CreatedAt: createdAt,
	}
}

func (p *Provider) hasCloudNAT(ctx context.Context, region string) (bool, error) {
	it := p.routersClient.List(ctx, &computepb.ListRoutersRequest{
		Project: p.project,
		Region:  region,
	})
	for {
		router, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return false, err
		}
		if len(router.GetNats()) > 0 {
			return true, nil
		}
	}
	return false, nil
}

func regionFromZone(zone string) string {
	if idx := strings.LastIndex(zone, "-"); idx != -1 {
		return zone[:idx]
	}
	return zone
}

func ptr[T any](v T) *T { return &v }
