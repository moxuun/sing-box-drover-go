#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/go-overlay.sh
source "$ROOT/scripts/go-overlay.sh"
OUTPUT="${1:-output/sing-box-drover}"
case "$OUTPUT" in
	/*) BINARY="$OUTPUT" ;;
	*) BINARY="$ROOT/$OUTPUT" ;;
esac

export GOTOOLCHAIN=go1.27.1
ARCH="${GOARCH:-$(go env GOARCH)}"
case "$ARCH" in
	amd64|arm64) ;;
	*) echo "Linux builds support amd64 or arm64" >&2; exit 1 ;;
esac
prepare_go_overlay "$ROOT"

PACKAGE_DIR="$(dirname "$BINARY")"
mkdir -p "$PACKAGE_DIR"
(
	cd "$ROOT"
	GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 \
		"$GO_BINARY" build -trimpath -ldflags="-s -w" -o "$BINARY" ./cmd/sing-box-drover
)
if [[ ! -f "$PACKAGE_DIR/sing-box-drover.ini" ]]; then
	cp "$ROOT/examples/sing-box-drover.ini" "$PACKAGE_DIR/sing-box-drover.ini"
fi

echo "Build complete: $BINARY (linux/$ARCH)"
echo "Requires GTK 3 and libayatana-appindicator3 or libappindicator3 in the desktop session"
echo "Add sing-box and config.json, then edit $PACKAGE_DIR/sing-box-drover.ini"
