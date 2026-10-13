#!/usr/bin/env bash
# Runs once as root on a clean Ubuntu 24.04: installs build tools and Go toolchain.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive

apt-get update -qq
apt-get install -y -qq --no-install-recommends build-essential curl ca-certificates

if ! command -v go >/dev/null 2>&1; then
  GO_VERSION="1.24.0"
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" | tar -C /usr/local -xz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

go version
