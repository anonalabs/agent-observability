#!/bin/sh
# Installs the latest agentobs release binary. No Go toolchain required.
#
#   curl -fsSL https://raw.githubusercontent.com/anonalabs/agent-observability/main/install.sh | sh
#
# Override install dir with AGENTOBS_INSTALL_DIR (default: $HOME/.local/bin).

set -eu

REPO="anonalabs/agent-observability"
INSTALL_DIR="${AGENTOBS_INSTALL_DIR:-$HOME/.local/bin}"

os() {
  uname_os=$(uname -s)
  case "$uname_os" in
    Linux) echo "linux" ;;
    Darwin) echo "darwin" ;;
    *) echo "unsupported OS: $uname_os" >&2; exit 1 ;;
  esac
}

arch() {
  uname_arch=$(uname -m)
  case "$uname_arch" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) echo "unsupported architecture: $uname_arch" >&2; exit 1 ;;
  esac
}

main() {
  os_name=$(os)
  arch_name=$(arch)
  archive="agentobs_${os_name}_${arch_name}.tar.gz"
  url="https://github.com/${REPO}/releases/latest/download/${archive}"

  tmp_dir=$(mktemp -d)
  trap 'rm -rf "$tmp_dir"' EXIT

  echo "Downloading ${archive}..."
  if ! curl -fsSL "$url" -o "$tmp_dir/$archive"; then
    echo "Failed to download $url" >&2
    echo "No release published yet? Build from source instead: see cli/README.md" >&2
    exit 1
  fi

  tar -xzf "$tmp_dir/$archive" -C "$tmp_dir"

  mkdir -p "$INSTALL_DIR"
  mv "$tmp_dir/agentobs" "$INSTALL_DIR/agentobs"
  chmod +x "$INSTALL_DIR/agentobs"

  echo "Installed agentobs to $INSTALL_DIR/agentobs"

  case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
      echo ""
      echo "$INSTALL_DIR is not on your PATH. Add this to your shell rc:"
      echo "  export PATH=\"\$PATH:$INSTALL_DIR\""
      ;;
  esac

  echo ""
  echo "Next: agentobs install && agentobs connect"
}

main
