package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/starlyn/reversproxy/internal/config"
)

func TestLoadServerConfig_MissingFile(t *testing.T) {
	cfg, err := config.LoadServerConfig("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	// Defaults should be populated — Addr field is gone; DataAddr takes its place.
	if cfg.DataAddr != ":8444" {
		t.Errorf("DataAddr: got %q, want %q", cfg.DataAddr, ":8444")
	}
	if cfg.AuthToken != "changeme" {
		t.Errorf("AuthToken: got %q, want %q", cfg.AuthToken, "changeme")
	}
}

func TestLoadServerConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")

	content := `
data_addr: ":9444"
auth_token: "supersecret"
log_level: "debug"
admin_addr: ":9091"
clients:
  - name: "edge-01"
    address: "10.0.0.1:8443"
    auth_token: "edge-token"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.LoadServerConfig(path)
	if err != nil {
		t.Fatalf("LoadServerConfig: %v", err)
	}

	if cfg.DataAddr != ":9444" {
		t.Errorf("DataAddr: got %q, want %q", cfg.DataAddr, ":9444")
	}
	if cfg.AuthToken != "supersecret" {
		t.Errorf("AuthToken: got %q, want %q", cfg.AuthToken, "supersecret")
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: got %q, want %q", cfg.LogLevel, "debug")
	}
	if cfg.AdminAddr != ":9091" {
		t.Errorf("AdminAddr: got %q, want %q", cfg.AdminAddr, ":9091")
	}
	if len(cfg.Clients) != 1 {
		t.Fatalf("Clients: got %d, want 1", len(cfg.Clients))
	}
	if cfg.Clients[0].Name != "edge-01" {
		t.Errorf("Clients[0].Name: got %q, want %q", cfg.Clients[0].Name, "edge-01")
	}
	if cfg.Clients[0].Address != "10.0.0.1:8443" {
		t.Errorf("Clients[0].Address: got %q, want %q", cfg.Clients[0].Address, "10.0.0.1:8443")
	}
}

func TestLoadServerConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")

	if err := os.WriteFile(path, []byte(":::invalid yaml:::"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := config.LoadServerConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadClientConfig_MissingFile(t *testing.T) {
	cfg, err := config.LoadClientConfig("/nonexistent/path/client.yaml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.ListenAddr != ":8443" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, ":8443")
	}
}

func TestLoadClientConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")

	content := `
listen_addr: "0.0.0.0:9443"
auth_token: "mytoken"
name: "my-client"
insecure: false
log_level: "warn"
tunnels:
  - type: tcp
    local_host: "127.0.0.1"
    local_port: 8080
    requested_port: 0
  - type: http
    hostname: "app.example.com"
    local_host: "127.0.0.1"
    local_port: 3000
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.LoadClientConfig(path)
	if err != nil {
		t.Fatalf("LoadClientConfig: %v", err)
	}

	if cfg.ListenAddr != "0.0.0.0:9443" {
		t.Errorf("ListenAddr: got %q, want %q", cfg.ListenAddr, "0.0.0.0:9443")
	}
	if cfg.AuthToken != "mytoken" {
		t.Errorf("AuthToken: got %q, want %q", cfg.AuthToken, "mytoken")
	}
	if cfg.Name != "my-client" {
		t.Errorf("Name: got %q, want %q", cfg.Name, "my-client")
	}
	if cfg.Insecure {
		t.Error("Insecure: got true, want false")
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel: got %q, want %q", cfg.LogLevel, "warn")
	}
	if len(cfg.Tunnels) != 2 {
		t.Fatalf("Tunnels: got %d, want 2", len(cfg.Tunnels))
	}
	if cfg.Tunnels[0].Type != "tcp" || cfg.Tunnels[0].LocalPort != 8080 {
		t.Errorf("Tunnels[0]: got %+v", cfg.Tunnels[0])
	}
	if cfg.Tunnels[1].Type != "http" || cfg.Tunnels[1].Hostname != "app.example.com" {
		t.Errorf("Tunnels[1]: got %+v", cfg.Tunnels[1])
	}
}

func TestDefaultServerConfig(t *testing.T) {
	cfg := config.DefaultServerConfig()
	if cfg.DataAddr == "" {
		t.Error("DefaultServerConfig: DataAddr is empty")
	}
	if cfg.AdminAddr == "" {
		t.Error("DefaultServerConfig: AdminAddr is empty")
	}
	if cfg.LogLevel == "" {
		t.Error("DefaultServerConfig: LogLevel is empty")
	}
}

func TestDefaultClientConfig(t *testing.T) {
	cfg := config.DefaultClientConfig()
	if cfg.ListenAddr == "" {
		t.Error("DefaultClientConfig: ListenAddr is empty")
	}
	if cfg.Name == "" {
		t.Error("DefaultClientConfig: Name is empty")
	}
}
