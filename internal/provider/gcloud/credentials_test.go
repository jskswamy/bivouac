package gcloud

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCredentials_BaseURL(t *testing.T) {
	t.Setenv("GCLOUD_API_URL", "http://127.0.0.1:8080")
	creds, err := ResolveCredentials(context.Background())
	if err != nil {
		t.Fatalf("ResolveCredentials() error = %v", err)
	}
	if creds.BaseURL != "http://127.0.0.1:8080" {
		t.Errorf("got BaseURL %q, want http://127.0.0.1:8080", creds.BaseURL)
	}
	opts := creds.ClientOptions()
	if len(opts) != 1 {
		t.Errorf("got %d client options, want 1", len(opts))
	}
}

func TestResolveCredentials_CredentialsFile(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/tmp/nonexistent-key.json")
	creds, err := ResolveCredentials(context.Background())
	if err != nil {
		t.Fatalf("ResolveCredentials() error = %v", err)
	}
	if creds.FilePath != "/tmp/nonexistent-key.json" {
		t.Errorf("got FilePath %q, want /tmp/nonexistent-key.json", creds.FilePath)
	}
	opts := creds.ClientOptions()
	if len(opts) != 1 {
		t.Errorf("got %d client options, want 1", len(opts))
	}
}

func TestResolveCredentials_ServiceAccountKeyPath(t *testing.T) {
	t.Setenv("GCLOUD_SERVICE_ACCOUNT_KEY", "/tmp/service-account.json")
	creds, err := ResolveCredentials(context.Background())
	if err != nil {
		t.Fatalf("ResolveCredentials() error = %v", err)
	}
	if creds.FilePath != "/tmp/service-account.json" {
		t.Errorf("got FilePath %q, want /tmp/service-account.json", creds.FilePath)
	}
}

func TestResolveCredentials_ServiceAccountKeyJSON(t *testing.T) {
	jsonKey := `{"type": "service_account", "project_id": "test-project"}`
	t.Setenv("GCLOUD_SERVICE_ACCOUNT_KEY", jsonKey)
	creds, err := ResolveCredentials(context.Background())
	if err != nil {
		t.Fatalf("ResolveCredentials() error = %v", err)
	}
	if string(creds.JSONData) != jsonKey {
		t.Errorf("got JSONData %q, want %q", string(creds.JSONData), jsonKey)
	}
}

func TestCredentials_SetupEnv_MaterializesFile(t *testing.T) {
	jsonKey := `{"type": "service_account", "project_id": "test-project"}`
	creds := &Credentials{
		JSONData: []byte(jsonKey),
	}

	cleanup, err := creds.SetupEnv()
	if err != nil {
		t.Fatalf("SetupEnv() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup function")
	}

	path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		t.Fatal("GOOGLE_APPLICATION_CREDENTIALS was not set")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("got file perm %04o, want 0600", perm)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != jsonKey {
		t.Errorf("file content mismatch: got %q, want %q", string(content), jsonKey)
	}

	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file %q still exists after cleanup", path)
	}
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		t.Error("GOOGLE_APPLICATION_CREDENTIALS still set after cleanup")
	}
}

func TestCredentials_SetupEnv_ExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	fakeKeyPath := filepath.Join(tmpDir, "key.json")
	if err := os.WriteFile(fakeKeyPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	creds := &Credentials{
		FilePath: fakeKeyPath,
	}

	cleanup, err := creds.SetupEnv()
	if err != nil {
		t.Fatalf("SetupEnv() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup function")
	}

	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != fakeKeyPath {
		t.Errorf("got %q, want %q", os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"), fakeKeyPath)
	}

	cleanup()
	if os.Getenv("GOOGLE_APPLICATION_CREDENTIALS") != "" {
		t.Error("GOOGLE_APPLICATION_CREDENTIALS still set after cleanup")
	}
}

func TestCredentials_ResolveAccessToken_EnvDirect(t *testing.T) {
	creds := &Credentials{
		AccessToken: "mock-token-1234",
	}
	tok, err := creds.ResolveAccessToken(context.Background())
	if err != nil {
		t.Fatalf("ResolveAccessToken() error = %v", err)
	}
	if tok != "mock-token-1234" {
		t.Errorf("got token %q, want mock-token-1234", tok)
	}
}
