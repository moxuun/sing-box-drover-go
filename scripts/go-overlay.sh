#!/usr/bin/env bash
# Prepares the golang/go#81956 memory workaround for Go 1.27.1 builds.
#
# Source this file, then call prepare_go_overlay <repo-root>. It resolves the
# toolchain, generates the overlay, and exports GO_BINARY, GO_OVERLAY_FILE and
# GOFLAGS so that every later build in the script picks the workaround up.
#
# The overlay key and the build must use the same GOROOT; a mismatch makes Go
# silently ignore the replacement and produce a 32 MiB binary.

prepare_go_overlay() {
	local root="$1"
	local goroot modcache
	goroot="$(go env GOROOT)"
	modcache="$(go env GOMODCACHE)"
	if [[ -z "$goroot" || -z "$modcache" ]]; then
		echo "Failed to locate the Go 1.27.1 toolchain" >&2
		return 1
	fi
	# Go rejects overlays beneath GOMODCACHE, including downloaded toolchains.
	if [[ "$goroot" == "$modcache"/* ]]; then
		local standalone="$root/output/.toolchains/go1.27.1"
		if [[ ! -d "$standalone" ]]; then
			mkdir -p "$(dirname "$standalone")"
			cp -R "$goroot" "$standalone"
		fi
		goroot="$standalone"
	fi
	GO_BINARY="$goroot/bin/go"
	GO_OVERLAY_FILE="$root/output/.go-overlay/overlay.json"
	mkdir -p "$(dirname "$GO_OVERLAY_FILE")"
	# The generator verifies the toolchain version and the source SHA-256, and
	# refuses to patch changed source.
	if ! (cd "$root" && GOROOT="$goroot" GOTOOLCHAIN=local "$GO_BINARY" \
		run ./tools/go-overlay -goroot "$goroot" -output "$GO_OVERLAY_FILE" >/dev/null); then
		echo "Go memory workaround preparation failed" >&2
		return 1
	fi
	# Go silently ignores an overlay whose key does not match the building
	# GOROOT, which would leave the 32 MiB buffer in place. Fail loudly instead.
	if ! grep -qF "$goroot/src/" "$GO_OVERLAY_FILE"; then
		echo "Overlay does not target the building GOROOT ($goroot)" >&2
		return 1
	fi
	export GO_BINARY
	export GO_OVERLAY_FILE
	export GOROOT="$goroot"
	export GOTOOLCHAIN=local
	# The workaround targets non-FIPS builds; the generator checks this too.
	export GOFIPS140=off
	GOFLAGS="${GOFLAGS:-} -overlay=$GO_OVERLAY_FILE"
	export GOFLAGS
}
