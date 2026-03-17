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

// ClientTarget describes a single client that the server will dial on startup.
type ClientTarget struct {
	// Name is a human-readable label for this client target.
	Name string `yaml:"name"`
	// Address is the host:port where the client is listening (e.g. "192.168.1.10:8443").
	Address string `yaml:"address"`
	// AuthToken is the pre-shared token sent to this client during handshake.
	AuthToken string `yaml:"auth_token"`
}

// ServerConfig holds all server-side configuration values.
// Values loaded from the YAML file are overridden by command-line flags in
// the caller; the zero value of each field means "use the default".
type ServerConfig struct {
	// DataAddr is the TCP data-connection listen address (default ":8444").
	DataAddr string `yaml:"data_addr"`
	// HTTPAddr is the plain-HTTP proxy listen address (empty = disabled).
	HTTPAddr string `yaml:"http_addr"`
	// HTTPSAddr is the HTTPS/SNI proxy listen address (empty = disabled).
	HTTPSAddr string `yaml:"https_addr"`
	// SOCKSAddr is the SOCKS5 proxy listen address (empty = disabled, default ":1080").
	SOCKSAddr string `yaml:"socks_addr"`
	// SOCKSUser is the username for SOCKS5 authentication (empty = no auth).
	SOCKSUser string `yaml:"socks_user"`
	// SOCKSPass is the password for SOCKS5 authentication (empty = no auth).
	SOCKSPass string `yaml:"socks_pass"`
	// AdminAddr is the admin HTTP API listen address (default ":9090").
	AdminAddr string `yaml:"admin_addr"`
	// AuthToken is the default pre-shared authentication token (default "changeme").
	// Used when a ClientTarget does not specify its own token.
	AuthToken string `yaml:"auth_token"`
	// CertPath is the path to the TLS certificate file.
	CertPath string `yaml:"cert_path"`
	// KeyPath is the path to the TLS private key file.
	KeyPath string `yaml:"key_path"`
	// LogLevel controls the verbosity of the logger (debug/info/warn/error).
	LogLevel string `yaml:"log_level"`
	// Clients is the list of client addresses the server will dial.
	// Each entry must have at minimum an Address field.
	Clients []ClientTarget `yaml:"clients"`
}

// DefaultServerConfig returns a ServerConfig populated with production-ready
// defaults.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		DataAddr:  ":8444",
		HTTPAddr:  ":8080",
		HTTPSAddr: ":8445",
		SOCKSAddr: ":1080",
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
	// ListenAddr is the address on which the client listens for server connections
	// (default ":8443").
	ListenAddr string `yaml:"listen_addr"`
	// AuthToken is the pre-shared token validated during server handshake.
	AuthToken string `yaml:"auth_token"`
	// Name is a human-readable label for this client.
	Name string `yaml:"name"`
	// Insecure skips TLS certificate verification (development only).
	Insecure bool `yaml:"insecure"`
	// Tunnels is the list of tunnels this client will register.
	Tunnels []TunnelConfig `yaml:"tunnels"`
	// LogLevel controls the verbosity of the logger (debug/info/warn/error).
	LogLevel string `yaml:"log_level"`
	// CertPath is the path to the TLS certificate file for the client listener.
	CertPath string `yaml:"cert_path"`
	// KeyPath is the path to the TLS private key file for the client listener.
	KeyPath string `yaml:"key_path"`
}

// DefaultClientConfig returns a ClientConfig populated with sensible defaults.
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		ListenAddr: ":8443",
		AuthToken:  "changeme",
		Name:       "client1",
		Insecure:   true,
		LogLevel:   "info",
		CertPath:   "client.crt",
		KeyPath:    "client.key",
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
