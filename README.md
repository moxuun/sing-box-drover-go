# sing-box-drover (Go rewrite)

A lightweight Windows tray controller for an external `sing-box.exe`.

This rewrite preserves the workflow of
[`hdrover/sing-box-drover`](https://github.com/hdrover/sing-box-drover) while
discovering selectors from the running Clash API. Runtime discovery allows
provider-backed selectors from compatible cores such as reF1nd to appear in
the tray without teaching the controller how to parse provider formats.

## Layout

- `cmd/sing-box-drover`: executable entry point.
- `internal/app`: application orchestration.
- `internal/config`: INI, JSON, TUN filtering, and BPF profile loading.
- `internal/clash`: selector discovery/switching and persisted choices.
- `internal/core`: owned `sing-box.exe` process lifecycle.
- `internal/windows`: tray UI, system proxy, elevation, single-instance, and autostart integration.

The source sing-box configuration remains the source of truth and is not
rewritten during normal mode switching.

## Build

The controller is a native Windows program and does not embed a sing-box core:

```powershell
go test ./...
$env:GOOS = "windows"; $env:GOARCH = "amd64"
go build -ldflags=-H=windowsgui ./cmd/sing-box-drover
```

Place the resulting executable, `sing-box.exe`, `sing-box-drover.ini`, and
`config.json` (or a `.bpf` profile) in the same directory. `sb-dir` and
`sb-config-file` may also point elsewhere. The controller starts the external
core with its runtime JSON on stdin; the source file is never rewritten for
TUN/system-proxy mode changes.

## First Windows test

Use a clean test directory containing the controller, the intended external
core, and the real JSON/BPF profile. Before the first launch, temporarily use
the following settings in `sing-box-drover.ini`:

```ini
system-proxy-auto = 0
log-file = sing-box-drover.log
```

Fully exit the old Drover before starting this build. Also stop any other
`sing-box.exe`, reF1nd, or compatible core that could use the configured mixed,
TUN, or Clash API ports; running two controllers/cores against the same ports
can look like a controller failure. Start with TUN off, inspect the log, then
test System Proxy, opening the selector menu, selector switching, and finally
the elevated TUN and Task Scheduler actions. Once the run is confirmed, set
`system-proxy-auto = 1` only if automatic proxy enable/cleanup is desired.

When the core exposes `experimental.clash_api`, the tray requests
`GET /proxies` whenever its menu opens. Every `type: "Selector"` is shown in
the API's order, with its `all` entries displayed verbatim (including
provider-backed entries); `now` is checked. Selecting an entry sends
`PUT /proxies/<selector>` and then asks the core to flush connections. A
temporary API failure leaves the last successful menu data in place.

The optional `selector-persist = 1` state is stored in
`sing-box-drover.state.json`. A saved entry is restored only if it still occurs
in that selector's current `all` list; otherwise the API's current `now` value
is kept.

The tray uses a normal click for System Proxy and Shift-click for TUN. TUN,
Task Scheduler autostart, and other privileged operations request elevation
only for the elevated relaunch. The child core is hidden, attached to a
Windows Job Object, monitored for unexpected exit, and stopped with Ctrl+C
before a bounded forced termination fallback.
