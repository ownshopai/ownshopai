#!/usr/bin/env sh
# Install and start ownshop in Termux:  curl -fsSL "$OWNSHOP_URL/install-termux.sh" | sh
set -eu
OWNSHOP_URL=${OWNSHOP_URL:-https://downloads.deemwar.com/ownshopai/termux}
curl -fL -o "$HOME/ownshop-linux-arm64.tar.gz" "$OWNSHOP_URL/ownshop-linux-arm64.tar.gz"
tar -xzf "$HOME/ownshop-linux-arm64.tar.gz" -C "$HOME" && rm -f "$HOME/ownshop-linux-arm64.tar.gz"
grep -q "alias ownshop=" "$HOME/.bashrc" 2>/dev/null || echo "alias ownshop='sh ~/ownshop/run-termux.sh'" >> "$HOME/.bashrc"
exec sh "$HOME/ownshop/run-termux.sh"
