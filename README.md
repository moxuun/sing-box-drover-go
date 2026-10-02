# sing-box-drover-go

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/moxuun/sing-box-drover-go)](https://github.com/moxuun/sing-box-drover-go/releases)

轻量的 Windows / macOS sing-box 托盘控制器。配合你自己准备好的 `sing-box` 内核与原生 `config.json`，在系统托盘中快捷开关系统代理、TUN 模式及切换出站节点。

> **提示**：本程序专为已有可用配置的用户设计，仅负责托盘控制与状态切换；不内置内核、不修改配置文件，也不提供订阅管理功能。

## 快速开始

1. 从 [Releases](https://github.com/moxuun/sing-box-drover-go/releases) 下载对应系统的压缩包并解压。
2. 将你的 `sing-box.exe`（macOS 为 `sing-box`）和 `config.json` 放入同级目录。
3. 双击运行 `sing-box-dover-go.exe`（macOS 运行 `sing-box-drover.app`）。程序无主窗口，常驻于托盘。
4. 右键托盘图标展开菜单，点击 `System Proxy` 即可开启系统代理。

更多操作、配置要求及排查方法请参考[使用说明](docs/usage_note.md)。

## 功能特点

- **便捷开关**：单击托盘图标切换系统代理；`Shift + 单击` 切换 TUN 模式（macOS 可在菜单中点击 `TUN`）。
- **节点切换**：菜单直接拉取运行中内核的 Selector 节点列表，支持 provider 展开节点，点击即可切换。
- **自动落点提示**：自动选择组（URLTest）会实时标注内核实际选中的落地节点。
- **进程守护**：内核异常退出自动告警，支持通过菜单 `Restart core` 一键重启内核。
- **开机自启**：支持通过菜单项随系统登录启动。
- **原生极轻**：基于系统原生 API 构建（Windows 原生 Win32 / macOS 原生 AppKit），不依赖复杂 GUI 框架或 Web 运行时。

## 平台支持

- **Windows 10/11 (amd64)**：原生 Win32 托盘、WinINet 系统代理、任务计划程序登录自启、按需 UAC 提权。
- **macOS 12+ (Universal)**：原生 AppKit 状态栏、`networksetup` 系统代理、LaunchAgent 登录自启、按需管理员提权，支持 Apple Silicon 及 Intel 设备。

## 内存占用

程序本体非常轻量，托盘进程日常内存占用约 4–8 MiB（工作集，不含 sing-box 内核本身）。

![Windows 任务管理器中的托盘内存占用](./docs/memory_usage.png)

## 菜单界面

![托盘菜单界面](./docs/menu.png)

## 相关文档

- [使用说明](docs/usage_note.md)：安装准备、功能操作、配置要求及常见排查
- [开发说明](docs/dev_note.md)：维护边界、设计记录与构建发布流程

## 与原版的关系

本项目是 [`hdrover/sing-box-drover`](https://github.com/hdrover/sing-box-drover) 的 Go 重构版。原版基于 Pascal 编写，且未能适配部分第三方内核通过 `provider` 展开的节点列表。Go 重构版改为直接从运行中内核的 API 动态读取出站节点。
