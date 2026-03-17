// Package config provides YAML-based configuration loading for the reverse
// proxy server and client, with flag-override support.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// -----------------------------------------------------------------------
// Server configuration
// -----------------------------------------------------------------------

// ServerConfig holds all server-side configuration values.
// Values loaded from the YAML file are overridden by command-line flags in
// the caller; the zero value of each field means "use the default".
type ServerConfig struct {
	// Addr is the TLS control-plane listen address (default ":8443").
	Addr string `yaml:"addr"`
	// DataAddr is the TCP data-connection listen address (default ":8444").
	DataAddr string `yaml:"data_addr"`
	// HTTPAddr is the plain-HTTP proxy listen address (empty = disabled).
	HTTPAddr string `yaml:"http_addr"`
	// HTTPSAddr is the HTTPS/SNI proxy listen address (empty = disabled).
	HTTPSAddr string `yaml:"https_addr"`
	// AdminAddr is the admin HTTP API listen address (default ":9090").
	AdminAddr string `yaml:"admin_addr"`
	// AuthToken is the pre-shared authentication token (default "changeme").
	AuthToken string `yaml:"auth_token"`
	// CertPath is the path to the TLS certificate file.
	CertPath string `yaml:"cert_path"`
	// KeyPath is the path to the TLS private key file.
	KeyPath string `yaml:"key_path"`
	// LogLevel controls the verbosity of the logger (debug/info/warn/error).
	LogLevel string `yaml:"log_level"`
}

// DefaultServerConfig returns a ServerConfig populated with production-ready
// defaults matching the pre-Phase 6 flag defaults.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		Addr:      ":8443",
		DataAddr:  ":8444",
		HTTPAddr:  ":8080",
		HTTPSAddr: ":8445",
		AdminAddr: ":9090",
		AuthToken: "changeme",
		CertPath:  "server.crt",
		KeyPath:   "server.key",
		LogLevel:  "info",
	}
}

// -----------------------------------------------------------------------
// Client configuration
// -----------------------------------------------------------------------

// TunnelConfig describes a single TCP tunnel to maintain.
type TunnelConfig struct {
	// LocalHost is the local service hostname (e.g. "127.0.0.1").
	LocalHost string `yaml:"local_host"`
	// LocalPort is the local service port.
	LocalPort int `yaml:"local_port"`
	// RequestedPort is the desired public port on the server (0 = any).
	RequestedPort int `yaml:"requested_port"`
	// Type is "tcp", "http", or "https".
	Type string `yaml:"type"`
	// Hostname is the virtual hostname for http/https tunnel types.
	Hostname string `yaml:"hostname"`
}

// ClientConfig holds all client-side configuration values.
type ClientConfig struct {
	// ServerAddr is the server control-plane address (default "localhost:8443").
	ServerAddr string `yaml:"server_addr"`
	// AuthToken is the pre-shared token sent during registration.
	AuthToken string `yaml:"auth_token"`
	// Name is a human-readable label for this client.
	Name string `yaml:"name"`
	// Insecure skips TLS certificate verification (development only).
	Insecure bool `yaml:"insecure"`
	// Tunnels is the list of tunnels this client will register.
	Tunnels []TunnelConfig `yaml:"tunnels"`
	// LogLevel controls the verbosity of the logger (debug/info/warn/error).
	LogLevel string `yaml:"log_level"`
}

// DefaultClientConfig returns a ClientConfig populated with sensible defaults.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		ServerAddr: "localhost:8443",
		AuthToken:  "changeme",
		Name:       "client1",
		Insecure:   true,
		LogLevel:   "info",
	}
}

// -----------------------------------------------------------------------
// Loading helpers
// -----------------------------------------------------------------------

// LoadServerConfig reads a YAML server config from path and merges it on top
// of the defaults. Fields that are absent in the file keep their default value.
// Returns an error if the file exists but cannot be parsed.
func LoadServerConfig(path string) (*ServerConfig, error) {
	cfg := DefaultServerConfig()
	if err := loadYAML(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadClientConfig reads a YAML client config from path and merges it on top
// of the defaults. Returns an error if the file exists but cannot be parsed.
func LoadClientConfig(path string) (*ClientConfig, error) {
	cfg := DefaultClientConfig()
	if err := loadYAML(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// loadYAML reads the YAML file at path and decodes it into dst.
// If the file does not exist, loadYAML is a no-op (returns nil).
func loadYAML(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // missing config file is not an error
		}
		return fmt.Errorf("config: open %q: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("config: parse %q: %w", path, err)
	}
	return nil
}
