// Code generated from Pkl module `bivouac.Config`. DO NOT EDIT.
package config

type GCloudConfig struct {
	Project string `pkl:"project"`

	Zone string `pkl:"zone"`

	MachineType string `pkl:"machineType"`

	Network string `pkl:"network"`

	Subnetwork *string `pkl:"subnetwork"`

	ExternalIP bool `pkl:"externalIP"`

	DiskSizeGb int `pkl:"diskSizeGb"`

	Image string `pkl:"image"`
}
