#!/usr/bin/env bash
# Usage: ./deploy/deploy.sh user@your-vps
# Cross-compiles locally, copies the single binary, restarts the service.
set -euo pipefail
host="${1:?usage: deploy.sh user@host}"
cd "$(dirname "$0")/.."

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o harvest-linux ./cmd/harvest
scp harvest-linux deploy/harvest.service "$host":/tmp/
rm harvest-linux

ssh -t "$host" '
  set -e
  id harvest >/dev/null 2>&1 || sudo useradd --system --no-create-home --shell /usr/sbin/nologin harvest
  sudo install -d /opt/harvest
  sudo install -m 755 /tmp/harvest-linux /opt/harvest/harvest
  sudo install -m 644 /tmp/harvest.service /etc/systemd/system/harvest.service
  if [ ! -f /etc/harvest.env ]; then
    printf "HARVEST_DOMAIN=harvest.ayaanshaikh.ca\n#GITHUB_TOKEN=\n#GITHUB_CLIENT_ID=\n#GITHUB_CLIENT_SECRET=\n" | sudo tee /etc/harvest.env >/dev/null
    sudo chmod 600 /etc/harvest.env
    echo "Created /etc/harvest.env - edit it to add your GitHub token/OAuth keys."
  fi
  sudo systemctl daemon-reload
  sudo systemctl enable --now harvest
  sudo systemctl restart harvest
  systemctl --no-pager status harvest | head -5
'
