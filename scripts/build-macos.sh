#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/go-overlay.sh
source "$ROOT/scripts/go-overlay.sh"
OUTPUT="${1:-output/sing-box-drover.app}"
case "$OUTPUT" in
	/*) APP="$OUTPUT" ;;
	*) APP="$ROOT/$OUTPUT" ;;
esac

VERSION="${VERSION:-dev}"
MACOS_MIN_VERSION="${MACOS_MIN_VERSION:-13.0}"
if [[ ! "$MACOS_MIN_VERSION" =~ ^([0-9]+)\.([0-9]+)(\.[0-9]+)?$ ]] || [[ "${BASH_REMATCH[1]:-0}" -lt 13 ]]; then
	echo "Go 1.27 requires macOS 13.0 or newer" >&2
	exit 1
fi
export GOTOOLCHAIN=go1.27.1
APP_CONTENTS="$APP/Contents"
APP_MACOS="$APP_CONTENTS/MacOS"
APP_RESOURCES="$APP_CONTENTS/Resources"
BINARY="$APP_MACOS/sing-box-drover"
PACKAGE_DIR="$(dirname "$APP")"
CONFIG_FILE="$PACKAGE_DIR/sing-box-drover.ini"
if [[ -n "${GOARCH:-}" ]]; then
	ARCHS=("$GOARCH")
else
	ARCHS=(arm64 amd64)
fi
BUILD_FILES=()

cleanup() {
	if ((${#BUILD_FILES[@]})); then
		rm -f "${BUILD_FILES[@]}"
	fi
}
trap cleanup EXIT

mkdir -p "$APP_MACOS" "$APP_RESOURCES" "$PACKAGE_DIR"
prepare_go_overlay "$ROOT"
sed -e "s/@VERSION@/$VERSION/g" -e "s/@MIN_MACOS_VERSION@/$MACOS_MIN_VERSION/g" \
	"$ROOT/resources/macos/Info.plist" > "$APP_CONTENTS/Info.plist"
if [[ ! -f "$CONFIG_FILE" ]]; then
	cp "$ROOT/examples/sing-box-drover.ini" "$CONFIG_FILE"
fi

for arch in "${ARCHS[@]}"; do
	output="$APP_MACOS/sing-box-drover.$arch"
	BUILD_FILES+=("$output")
	(
		cd "$ROOT"
		GOOS=darwin GOARCH="$arch" CGO_ENABLED=0 \
			"$GO_BINARY" build -trimpath -ldflags="-s -w -macos=$MACOS_MIN_VERSION" -o "$output" ./cmd/sing-box-drover
	)
done

if ((${#ARCHS[@]} == 1)); then
	mv "${BUILD_FILES[0]}" "$BINARY"
	BUILD_FILES=()
else
	lipo -create "${BUILD_FILES[@]}" -output "$BINARY"
	rm -f "${BUILD_FILES[@]}"
	BUILD_FILES=()
fi

minos_values="$(otool -l "$BINARY" | awk '$1 == "minos" { print $2 }' | sort -u | tr '\n' ' ')"
if [[ "$minos_values" != "$MACOS_MIN_VERSION " ]]; then
	echo "Build error: unexpected macOS deployment target(s): ${minos_values:-none}" >&2
	echo "Expected: $MACOS_MIN_VERSION" >&2
	exit 1
fi

if command -v codesign >/dev/null 2>&1; then
	codesign --force --deep --sign - "$APP" >/dev/null
fi

echo "Build complete: $APP"
echo "Architectures: $(lipo -archs "$BINARY")"
echo "Minimum macOS: $MACOS_MIN_VERSION"
echo "Add sing-box and config.json, then edit $CONFIG_FILE"
