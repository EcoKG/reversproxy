# reversproxy

Go reverse tunnel proxy. Client listens, server dials — NAT/firewall traversal without port forwarding.

```
[External User] --> [Server:9000] --tunnel--> [Client] --> [Local Service :80]
                         |                        ^
                         +--- TLS Dial -----------+
```

## Install

```bash
go install github.com/EcoKG/reversproxy/cmd/client@latest
go install github.com/EcoKG/reversproxy/cmd/server@latest
```

Or download binaries from [Releases](https://github.com/EcoKG/reversproxy/releases).

## Quick Start

### 1. Client (Linux — listens and waits)

```bash
reversproxy-client --listen :8443 --token secret --name myserver --local-port 80
```

### 2. Server (Windows/Linux — dials to client)

```yaml
# server-config.yaml
data_addr: ":8444"
http_addr: ":8080"
admin_addr: ":9090"
auth_token: "secret"
clients:
  - name: "myserver"
    address: "192.168.1.10:8443"
    auth_token: "secret"
```

```bash
reversproxy-server --config server-config.yaml
```

## Features

- **Reverse connection**: Client listens, server connects to client
- **TCP tunneling**: Forward any TCP port through the tunnel
- **HTTP/HTTPS routing**: Host-header and SNI-based routing
- **Auto-reconnect**: Server retries with exponential backoff
- **Multi-client**: Multiple clients with independent tunnels
- **Admin API**: `GET /api/clients`, `/api/tunnels`, `/api/stats`
- **TLS 1.3**: All control connections encrypted
- **YAML config**: File-based configuration with flag overrides

## Build

```bash
# Native
go build -o reversproxy-client ./cmd/client
go build -o reversproxy-server ./cmd/server

# Cross-compile
GOOS=linux GOARCH=amd64 go build -o reversproxy-client-linux ./cmd/client
GOOS=windows GOARCH=amd64 go build -o reversproxy-server.exe ./cmd/server
```

## Ports

| Port | Purpose | Side |
|------|---------|------|
| `:8443` | Client TLS listener | Client |
| `:8444` | Tunnel data connections | Server |
| `:8080` | HTTP host-based proxy | Server |
| `:8445` | HTTPS SNI proxy | Server |
| `:9090` | Admin API | Server |

## License

MIT
