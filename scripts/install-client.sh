#!/bin/bash
set -e

REPO="EcoKG/reversproxy"
VERSION="${1:-latest}"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/reversproxy"
SERVICE_USER="reversproxy"

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  armv7l)  ARCH="arm" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "==> reversproxy client installer"
echo "    OS: linux / Arch: $ARCH"

# Get download URL
if [ "$VERSION" = "latest" ]; then
  URL="https://github.com/$REPO/releases/latest/download/reversproxy-client-linux-$ARCH"
else
  URL="https://github.com/$REPO/releases/download/$VERSION/reversproxy-client-linux-$ARCH"
fi

# Download binary
echo "==> Downloading from $URL"
curl -fsSL -o /tmp/reversproxy-client "$URL"
chmod +x /tmp/reversproxy-client

# Install binary
echo "==> Installing to $INSTALL_DIR/reversproxy-client"
sudo mv /tmp/reversproxy-client "$INSTALL_DIR/reversproxy-client"

# Create config directory
sudo mkdir -p "$CONFIG_DIR"

# Create default config if not exists
if [ ! -f "$CONFIG_DIR/client.yaml" ]; then
  echo "==> Creating default config at $CONFIG_DIR/client.yaml"
  sudo tee "$CONFIG_DIR/client.yaml" > /dev/null <<'YAML'
# reversproxy client configuration
listen_addr: ":8443"
auth_token: "changeme"
name: "client1"
log_level: "info"
cert_path: "/etc/reversproxy/client.crt"
key_path: "/etc/reversproxy/client.key"

# Tunnels to register when server connects
tunnels:
  - type: tcp
    local_host: "127.0.0.1"
    local_port: 80
    requested_port: 9000
  # - type: http
  #   hostname: "myapp.example.com"
  #   local_host: "127.0.0.1"
  #   local_port: 8080
YAML
fi

# Create systemd service
echo "==> Installing systemd service"
sudo tee /etc/systemd/system/reversproxy-client.service > /dev/null <<'UNIT'
[Unit]
Description=ReverseProxy Tunnel Client
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/reversproxy-client --config /etc/reversproxy/client.yaml
Restart=always
RestartSec=5
LimitNOFILE=65536
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable reversproxy-client

echo ""
echo "==> Installation complete!"
echo ""
echo "    1. Edit config:  sudo nano /etc/reversproxy/client.yaml"
echo "    2. Start:        sudo systemctl start reversproxy-client"
echo "    3. Status:       sudo systemctl status reversproxy-client"
echo "    4. Logs:         journalctl -u reversproxy-client -f"
echo ""
