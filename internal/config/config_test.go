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
	// Defaults should be populated.
	if cfg.Addr != ":8443" {
		t.Errorf("Addr: got %q, want %q", cfg.Addr, ":8443")
	}
	if cfg.AuthToken != "changeme" {
		t.Errorf("AuthToken: got %q, want %q", cfg.AuthToken, "changeme")
	}
}

func TestLoadServerConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.yaml")

	content := `
addr: ":9443"
auth_token: "supersecret"
log_level: "debug"
admin_addr: ":9091"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.LoadServerConfig(path)
	if err != nil {
		t.Fatalf("LoadServerConfig: %v", err)
	}

	if cfg.Addr != ":9443" {
		t.Errorf("Addr: got %q, want %q", cfg.Addr, ":9443")
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
	// Non-specified field should keep its default.
	if cfg.DataAddr != ":8444" {
		t.Errorf("DataAddr: got %q, want %q", cfg.DataAddr, ":8444")
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
	if cfg.ServerAddr != "localhost:8443" {
		t.Errorf("ServerAddr: got %q, want %q", cfg.ServerAddr, "localhost:8443")
	}
}

func TestLoadClientConfig_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "client.yaml")

	content := `
server_addr: "proxy.example.com:8443"
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

	if cfg.ServerAddr != "proxy.example.com:8443" {
		t.Errorf("ServerAddr: got %q, want %q", cfg.ServerAddr, "proxy.example.com:8443")
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
	if cfg.Addr == "" {
		t.Error("DefaultServerConfig: Addr is empty")
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
	if cfg.ServerAddr == "" {
		t.Error("DefaultClientConfig: ServerAddr is empty")
	}
	if cfg.Name == "" {
		t.Error("DefaultClientConfig: Name is empty")
	}
}
