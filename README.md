# sing-box-dover-go（Go 重构版）

这是一个轻量的 Windows 托盘控制器，负责启动和控制外置的
`sing-box.exe` 内核。

本项目保留 [`hdrover/sing-box-drover`](https://github.com/hdrover/sing-box-drover)
的使用方式，并从运行中的 Clash API 动态读取选择器。这样，reF1nd 等兼容
内核通过 provider 展开的节点也能直接显示在托盘菜单中，控制器不需要理解
各种 provider 文件格式。

## 重构原因

- 不适配 [`reF1nd`](https://github.com/reF1nd/sing-box) 内核 `config.json` 的
  `provider` 写法，托盘菜单节点显示不全。
- 原项目用 `Pascal` 语言编写，编译环境过大，难以维护。

## 内存占用

托盘控制器通常约 4–8 MiB（Windows 任务管理器的工作集，不包含 sing-box
内核；实际值会随系统和配置变化）。这个结果基于 Go 1.25.6 的 Windows amd64
构建。

![Windows 任务管理器中的托盘内存占用](./docs/memory_usage.png)

## 菜单界面

![托盘菜单界面](./docs/menu.png)

sing-box 原始配置始终是配置真源。普通模式切换不会改写它。

## 文档

- 用户使用、配置和首次测试见 [使用说明](docs/usage_note.md)；
- 目录、构建和自动发布见 [开发说明](docs/dev_note.md)。
