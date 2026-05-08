package control

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// KnownHosts persists per-client TLS leaf certificate fingerprints so that
// after a TOFU approval the same client is auto-trusted on subsequent
// connections. The on-disk format is YAML:
//
//	hosts:
//	  - name: web-server
//	    fingerprint: "sha256:abcd...ef"
//	  - name: db-server
//	    fingerprint: "sha256:0123...89"
type KnownHosts struct {
	path  string
	mu    sync.RWMutex
	hosts map[string]string // clientName → fingerprint
}

type knownHostsFile struct {
	Hosts []knownHost `yaml:"hosts"`
}

type knownHost struct {
	Name        string `yaml:"name"`
	Fingerprint string `yaml:"fingerprint"`
}

// LoadKnownHosts reads path if it exists. A missing file is not an error —
// it returns an empty store backed by path so that subsequent Add calls
// create it.
func LoadKnownHosts(path string) (*KnownHosts, error) {
	k := &KnownHosts{path: path, hosts: make(map[string]string)}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return k, nil
	}
	if err != nil {
		return nil, fmt.Errorf("known_hosts: read %s: %w", path, err)
	}

	var f knownHostsFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("known_hosts: parse %s: %w", path, err)
	}
	for _, h := range f.Hosts {
		if h.Name != "" && h.Fingerprint != "" {
			k.hosts[h.Name] = h.Fingerprint
		}
	}
	return k, nil
}

// Lookup returns the stored fingerprint for name and whether it exists.
func (k *KnownHosts) Lookup(name string) (string, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	fp, ok := k.hosts[name]
	return fp, ok
}

// Add stores fingerprint for name and atomically rewrites the backing file.
func (k *KnownHosts) Add(name, fingerprint string) error {
	k.mu.Lock()
	k.hosts[name] = fingerprint
	err := k.writeLocked()
	k.mu.Unlock()
	return err
}

// Remove drops name from the store and rewrites the backing file.
func (k *KnownHosts) Remove(name string) error {
	k.mu.Lock()
	delete(k.hosts, name)
	err := k.writeLocked()
	k.mu.Unlock()
	return err
}

// List returns all stored entries sorted by name.
func (k *KnownHosts) List() []knownHost {
	k.mu.RLock()
	out := make([]knownHost, 0, len(k.hosts))
	for n, fp := range k.hosts {
		out = append(out, knownHost{Name: n, Fingerprint: fp})
	}
	k.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// writeLocked persists the in-memory map to disk. Caller must hold k.mu.
func (k *KnownHosts) writeLocked() error {
	if k.path == "" {
		return nil
	}
	out := knownHostsFile{Hosts: make([]knownHost, 0, len(k.hosts))}
	for n, fp := range k.hosts {
		out.Hosts = append(out.Hosts, knownHost{Name: n, Fingerprint: fp})
	}
	sort.Slice(out.Hosts, func(i, j int) bool { return out.Hosts[i].Name < out.Hosts[j].Name })

	data, err := yaml.Marshal(&out)
	if err != nil {
		return fmt.Errorf("known_hosts: marshal: %w", err)
	}

	if dir := filepath.Dir(k.path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}

	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("known_hosts: write temp: %w", err)
	}
	if err := os.Rename(tmp, k.path); err != nil {
		return fmt.Errorf("known_hosts: rename: %w", err)
	}
	return nil
}

// Fingerprint computes the canonical "sha256:HEX" string for an x509 cert.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ShortFingerprint returns the first 16 hex chars after the algorithm prefix
// for compact display in log lines.
func ShortFingerprint(fp string) string {
	if i := strings.Index(fp, ":"); i >= 0 && len(fp) > i+17 {
		return fp[:i+17]
	}
	return fp
}
