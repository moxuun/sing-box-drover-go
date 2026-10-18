# sing-box-drover-go

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/moxuun/sing-box-drover-go)](https://github.com/moxuun/sing-box-drover-go/releases)

使用 [mygo](https://github.com/egoist/mygo) 的 Windows、macOS、Linux sing-box 托盘控制器。配合自行准备的 `sing-box` 内核与原生 `config.json`，在托盘中开关系统代理、TUN 模式及切换出站节点。

程序负责内核启停、状态显示和桌面集成，不内置内核，也不提供订阅管理或配置编辑。

## 快速开始

1. 从 [Releases](https://github.com/moxuun/sing-box-drover-go/releases) 下载对应平台的产物，或按下文从源码构建。
2. 将 `sing-box.exe`（Windows）或 `sing-box`（macOS/Linux）、`config.json` 和 `sing-box-drover.ini` 放在控制器旁边。
3. 运行 `sing-box-drover-go.exe`、`sing-box-drover.app` 或 Linux 的 `sing-box-drover`。程序无主窗口，常驻托盘。
4. 展开菜单，通过 `System Proxy`、`TUN` 和节点列表控制内核。

更多操作、配置要求及排查方法见[使用说明](docs/usage_note.md)。

## 功能

- **节点选择**：从运行中内核的 Clash API 读取 Selector，支持 provider 展开节点及 URLTest 当前落点提示。
- **内核管理**：启动、重启、异常退出提示；重启和 TUN 切换前检查新配置，退出时停止内核并清理接管的系统代理。
- **桌面集成**：共享托盘菜单、状态图标、通知、Homepage 和休眠恢复；各平台保留适合自身的自启动、系统代理和权限处理。
- **Windows 快捷操作**：单击图标切换系统代理，Shift + 单击切换 TUN。macOS 和 Linux 使用菜单。
- **原生界面**：mygo 调用系统托盘 API，无 WebView 或浏览器运行时，构建时关闭 CGO。

## 平台支持

| 平台 | 架构与要求 | 系统集成 |
| --- | --- | --- |
| Windows 10/11 | amd64 | WinINet 系统代理、任务计划程序自启动、按需 UAC |
| macOS 13+ | arm64 + amd64，Universal `.app` | `networksetup` 系统代理、LaunchAgent 自启动、管理员授权 |
| Linux | amd64、arm64；GTK 3、`libayatana-appindicator3` 或 `libappindicator3` | XDG 自启动；GNOME 系统代理；具备权限的 sing-box 可开启 TUN |

Linux 需要桌面会话支持 StatusNotifier/AppIndicator。GNOME 通常需要 AppIndicator 扩展；KDE、Xfce 或 Wayland 桌面需要相应托盘宿主。系统代理目前支持 GNOME 的 GSettings；其他桌面可通过 TUN 或应用自身的代理设置使用 sing-box，菜单中不支持的系统代理操作会返回明确提示。

Linux TUN 要求 sing-box 具有 `CAP_NET_ADMIN`。控制器会在启动或替换内核前检查权限；正常使用时运行普通用户托盘。配置权限的方法见[使用说明](docs/usage_note.md#linux)。

## 从源码构建

固定工具链为 **Go 1.27.2**，mygo 为 **v0.4.8**（其 `go.mod` 要求 Go 1.27.1，1.27.2 满足要求）。

```powershell
# Windows：包含应用图标、manifest 和 Go 内存问题的 overlay
.\scripts\build.ps1
```

```bash
# macOS：需 macOS 上的 lipo 和 codesign，默认生成 Universal .app
./scripts/build-macos.sh

# Linux：默认当前架构，也可设置 GOARCH=amd64 或 GOARCH=arm64
./scripts/build-linux.sh
```

已有测试与静态检查：

```bash
GOTOOLCHAIN=go1.27.2 go test ./...
GOTOOLCHAIN=go1.27.2 go vet ./...
```

三端 CI 使用同一工具链；标签发布生成 Windows ZIP、macOS Universal ZIP 和 Linux 两种架构的 tar.gz。详细构建及验证记录见[开发说明](docs/dev_note.md)。

## Go 版本和内存

Go 1.27.2 仍受 [golang/go#81956](https://github.com/golang/go/issues/81956) 影响：FIPS 熵源的 32 MiB 静态缓冲区会增加 Windows 私有提交内存。Windows 构建脚本使用校验版本及源码哈希的 overlay，将缓冲区改为按需分配，并保持 FIPS 关闭。升级 Go 时需要先复核该补丁。

Go 1.27.1 时的 Windows 构建比较确认补丁移除了 PE 数据段中的约 32 MiB 静态占用；1.27.2 只验证了目标源文件哈希一致、overlay 已应用到 Linux 构建，Windows 与 macOS 产物未在 1.27.2 下重测。真实工作集、私有提交内存及各桌面的完整交互还需要实机验证。历史内存截图和菜单截图不作为当前 mygo 版本的性能或外观保证。

## 相关文档

- [使用说明](docs/usage_note.md)：准备文件、桌面依赖、配置及排查
- [开发说明](docs/dev_note.md)：维护边界、构建发布及历史记录
- [mygo 迁移记录](docs/mygo-migration.md)：迁移范围、Go 补丁及验证结果

## 与原版的关系

本项目是 [`hdrover/sing-box-drover`](https://github.com/hdrover/sing-box-drover) 的 Go 重构版，改为从运行中内核的 API 动态读取出站节点，支持第三方内核通过 provider 展开的节点列表。
