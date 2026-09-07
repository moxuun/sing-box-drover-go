# AGENTS.md

## Goal

Rewrite `hdrover/sing-box-drover` in Go while preserving its lightweight design and existing behavior.

Reference implementation:

https://github.com/hdrover/sing-box-drover

This is a functional reimplementation, not a new proxy client and not a line-by-line Pascal translation.

When behavior is unclear, inspect the upstream project first.

## Main Requirements

Preserve the existing sing-box-drover functionality, including:

* lightweight Windows system tray UI;
* external `sing-box.exe`;
* support for compatible custom cores such as reF1nd;
* native sing-box JSON configuration;
* `.bpf` profile support and remote BPF updates;
* System Proxy toggle;
* TUN toggle;
* normal tray click toggles System Proxy;
* Shift + tray click toggles TUN;
* automatic System Proxy on startup/cleanup on exit;
* TUN startup mode;
* selector discovery and switching through Clash API;
* selector persistence;
* `auto`, `flat`, and `nested` selector menu layouts;
* restart core;
* autostart through Windows Task Scheduler;
* elevation when TUN or privileged operations require it;
* hidden sing-box process;
* graceful core shutdown;
* detection of unexpected core exit;
* useful error notifications and logs;
* prevention of orphaned sing-box processes.

Do not remove an upstream feature merely because the new implementation could be simpler without it.

## Scope

The application is a thin Windows controller around sing-box.

Do not turn it into:

* a subscription management client;
* a full configuration editor;
* a traffic dashboard;
* a large all-in-one proxy client.

Complex monitoring can remain in an external Web Dashboard.

## Technology

Use:

* Go 1.25+;
* Go standard library where possible;
* `golang.org/x/sys/windows`;
* native Win32 APIs.

Prefer a direct Win32 tray implementation or a very small native dependency.

Do not use without explicit approval:

* Electron;
* Chromium;
* WebView2;
* Tauri;
* Wails;
* Fyne;
* Qt;
* GTK;
* Avalonia;
* Node.js runtime;
* Python runtime.

The program should remain small and low-memory.

## Configuration

The user's sing-box configuration is the source of truth.

Do not convert it into a proprietary internal format.

Do not unnecessarily rewrite or normalize the original configuration.

Only parse fields the application actually needs, such as:

* `mixed` inbound;
* `tun` inbound;
* selector outbounds;
* `experimental.clash_api`.

Unknown sing-box fields must be preserved.

The application must remain tolerant of newer sing-box versions and compatible forks.

## Runtime Behavior

System Proxy and TUN switching may create an in-memory or temporary runtime configuration.

The original source configuration should remain unchanged during normal mode switching.

Prefer preserving the upstream approach of running sing-box with generated configuration through stdin when practical.

Selector switching must use the running Clash API rather than modifying the source config.

## Core Management

The tray application owns the sing-box child process.

It should:

* start it without a visible console window;
* monitor whether it is still running;
* capture bounded stdout/stderr for diagnostics;
* attempt graceful shutdown before forced termination;
* avoid leaving orphaned processes;
* report useful startup/crash errors.

Use Windows Job Objects or an equivalent mechanism where appropriate.

## Windows Integration

Preserve the original tray interactions and Windows behavior.

System Proxy changes must correctly notify Windows.

TUN should request elevation only when necessary.

Avoid permanently running the entire tray application as administrator if it can be avoided.

Only one controller instance should normally run.

## Compatibility First

Before reimplementing an existing feature:

1. inspect the relevant upstream source;
2. determine its observable behavior;
3. reproduce that behavior in Go;
4. then simplify or improve the internal implementation.

When upstream behavior and a cleaner redesign conflict, preserve upstream behavior unless the project owner explicitly approves the change.

## Code Style

Keep the code simple and easy for AI-assisted maintenance.

Prefer:

* small focused packages;
* explicit state;
* straightforward error handling;
* standard Go conventions;
* minimal dependencies.

Avoid:

* unnecessary abstraction layers;
* dependency-injection frameworks;
* excessive interfaces;
* reflection;
* clever generic code;
* unrelated refactors during bug fixes.

Run before considering work complete:

```text
gofmt
go vet ./...
go test ./...
```

## Testing

Prioritize regression tests for:

* config parsing;
* TUN filtering;
* mode switching;
* selector parsing and API calls;
* selector persistence;
* core lifecycle;
* BPF parsing/updating;
* failure cleanup.

When fixing a bug, add a regression test when practical.

## Definition of Success

An existing sing-box-drover user should be able to move to this implementation while keeping the same general workflow:

```text
external sing-box.exe
+
existing config.json / .bpf
+
small tray controller
```

with System Proxy, TUN, selectors, autostart and core management behaving as expected.

Keep the project small.

Keep sing-box in control of proxy behavior.

Keep the tray controller out of the way.
