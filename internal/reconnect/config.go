package reconnect

// TunnelConfig holds the parameters needed to (re-)register a single tunnel
// with the proxy server after a reconnection.
type TunnelConfig struct {
	// LocalHost is the hostname of the local service to expose (e.g. "127.0.0.1").
	LocalHost string
	// LocalPort is the port of the local service to expose (e.g. 8080).
	LocalPort int
	// RequestedPort is an optional hint for which public port to use (0 = any).
	RequestedPort int
}

// HTTPTunnelConfig holds the parameters needed to register an HTTP
// host-based tunnel.
type HTTPTunnelConfig struct {
	// Hostname is the virtual host to register for HTTP routing.
	Hostname string
	// LocalHost is the hostname/IP of the local service.
	LocalHost string
	// LocalPort is the port of the local service.
	LocalPort int
}

// HTTPSTunnelConfig holds the parameters needed to register an HTTPS
// SNI-based tunnel.
type HTTPSTunnelConfig struct {
	// Hostname is the SNI hostname to register for HTTPS routing.
	Hostname string
	// LocalHost is the hostname/IP of the local TLS service.
	LocalHost string
	// LocalPort is the port of the local TLS service.
	LocalPort int
}

// ClientConfig holds all the state the client needs to re-establish its
// connection and tunnel registrations after a reconnect.
type ClientConfig struct {
	// Tunnels is the list of tunnels the client wants to maintain.
	// The client re-registers all of them after every successful reconnection.
	Tunnels []TunnelConfig
	// HTTPTunnels is the list of HTTP host-based tunnels to register.
	HTTPTunnels []HTTPTunnelConfig
	// HTTPSTunnels is the list of HTTPS SNI-based tunnels to register.
	HTTPSTunnels []HTTPSTunnelConfig
}

// AddTunnel appends a tunnel registration request to the config.
func (c *ClientConfig) AddTunnel(localHost string, localPort, requestedPort int) {
	c.Tunnels = append(c.Tunnels, TunnelConfig{
		LocalHost:     localHost,
		LocalPort:     localPort,
		RequestedPort: requestedPort,
	})
}

// AddHTTPTunnel appends an HTTP hostname registration to the config.
func (c *ClientConfig) AddHTTPTunnel(hostname, localHost string, localPort int) {
	c.HTTPTunnels = append(c.HTTPTunnels, HTTPTunnelConfig{
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
	})
}

// AddHTTPSTunnel appends an HTTPS SNI hostname registration to the config.
func (c *ClientConfig) AddHTTPSTunnel(hostname, localHost string, localPort int) {
	c.HTTPSTunnels = append(c.HTTPSTunnels, HTTPSTunnelConfig{
		Hostname:  hostname,
		LocalHost: localHost,
		LocalPort: localPort,
	})
}
