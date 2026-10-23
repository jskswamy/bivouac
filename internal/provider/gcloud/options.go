package gcloud

import (
	"net/http"

	"google.golang.org/api/option"
)

// Option configures a Google Cloud provider.
type Option func(*options)

type options struct {
	baseURL         string
	credentialsJSON []byte
	credentialsFile string
	httpClient      *http.Client
	diskSizeGb      int
	externalIP      bool
	extraClientOpts []option.ClientOption
}

// WithBaseURL overrides the API endpoint URL (primarily for testing with gcpfake).
func WithBaseURL(url string) Option {
	return func(o *options) {
		o.baseURL = url
	}
}

// WithCredentialsJSON supplies service account key credentials as raw JSON bytes.
func WithCredentialsJSON(data []byte) Option {
	return func(o *options) {
		o.credentialsJSON = data
	}
}

// WithCredentialsFile supplies the path to a service account key file.
func WithCredentialsFile(path string) Option {
	return func(o *options) {
		o.credentialsFile = path
	}
}

// WithHTTPClient sets a custom http.Client for API calls.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) {
		o.httpClient = client
	}
}

// WithDiskSizeGb sets the default disk size in GB for the provider.
func WithDiskSizeGb(size int) Option {
	return func(o *options) {
		o.diskSizeGb = size
	}
}

// WithExternalIP configures whether instances created by the provider receive an external public IP.
func WithExternalIP(externalIP bool) Option {
	return func(o *options) {
		o.externalIP = externalIP
	}
}

// WithClientOptions passes additional low-level Google API options.
func WithClientOptions(opts ...option.ClientOption) Option {
	return func(o *options) {
		o.extraClientOpts = append(o.extraClientOpts, opts...)
	}
}
