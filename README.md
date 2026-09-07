# sing-box-drover（Go 重写版）

这是一个轻量的 Windows 托盘控制器，负责启动和控制外置的
`sing-box.exe`。

本项目保留 [`hdrover/sing-box-drover`](https://github.com/hdrover/sing-box-drover)
的使用方式，并从运行中的 Clash API 动态读取选择器。这样，reF1nd 等兼容
内核通过 provider 展开的节点也能直接显示在托盘菜单中，控制器不需要理解
各种 provider 文件格式。

## 目录结构

- `cmd/sing-box-drover`：程序入口；
- `internal/app`：应用编排；
- `internal/config`：INI、JSON、TUN 过滤和 BPF 配置读取；
- `internal/clash`：选择器读取、切换和状态持久化；
- `internal/core`：所管理的 `sing-box.exe` 生命周期；
- `internal/tray`：原生 Win32 托盘和选择器菜单渲染；
- `internal/windows`：系统代理、提权、单实例和开机启动；
- `docs`：开发日志、设计决定和审查记录；
- `tools`：国旗资源生成器等离线开发工具；
- `examples`：纳入版本控制的示例配置；
- `output`：不纳入版本控制的构建与本地运行目录。

sing-box 原始配置始终是配置真源。普通模式切换不会改写它。

## 构建

托盘控制器是原生 Windows 程序，不内置 sing-box 内核：

```powershell
$env:GOTOOLCHAIN = "go1.25.6"
go test ./...
go vet ./...
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -trimpath -ldflags="-s -w -H=windowsgui" -o .\output\sing-box-drover.exe .\cmd\sing-box-drover
```

`go.mod` 暂时将 Go 1.25.6 固定为低内存构建基线。`toolchain` 指令不会让
已经运行的 Go 1.27 自动降级，因此需要在 PowerShell 中设置
`$env:GOTOOLCHAIN = "go1.25.6"`。

本地复现表明，Go 1.26 和 Go 1.27 在 `net/http` 等 TLS 路径中会引入
32 MiB 的 FIPS 140 熵源缓冲区（`crypto/internal/fips140/drbg.memory`）。
即使未启用 FIPS，该缓冲区在 Windows 上也会成为驻留私有内存，使托盘进程
额外占用约 34 MB。上游问题 `golang/go#78321` 记录了 Wasm 场景；Windows
工作集结论来自本地复现。在确认新版工具链修复前，不要把 Go 1.27 当成解决
方案。

本地运行文件统一放在 `output`。将 `sing-box.exe`、从
`examples/sing-box-drover.ini` 复制并改名得到的 `sing-box-drover.ini`，以及
`config.json`（或 `.bpf` 配置）放在控制器旁边。也可以通过 `sb-dir` 和
`sb-config-file` 指向其他位置。控制器通过标准输入把运行时 JSON 交给内核，
切换 TUN 或系统代理时不会改写源配置。

托盘里的 `Homepage` 会打开 `sing-box-drover.ini` 中的 `homepage-url`。该项
接受 `http://` 或 `https://` 地址，例如可直接填写本地 Web 面板
`http://127.0.0.1:9090/ui/`。

## 首次在 Windows 上测试

准备一个干净的测试目录，放入控制器、目标内核和真实 JSON/BPF 配置。首次
启动前，建议在 `sing-box-drover.ini` 中临时使用：

```ini
system-proxy-auto = 0
log-file = sing-box-drover.log
```

启动前应完整退出旧 Drover，并停止会占用相同 mixed、TUN 或 Clash API 端口
的其他 `sing-box.exe`、reF1nd 或兼容内核。先关闭 TUN 启动并查看日志，再依次
测试系统代理、打开选择器菜单、切换节点，最后测试需要提权的 TUN 和任务计划
程序。确认无误后，如果需要启动时自动开启、退出时自动清理系统代理，再把
`system-proxy-auto` 改为 `1`。

内核提供 `experimental.clash_api` 时，托盘打开菜单会请求 `GET /proxies`。
所有 `type: "Selector"` 项按 API 返回顺序显示，其 `all` 列表保持原样，包括
provider 展开的节点；当前 `now` 项会被勾选。选择节点后，控制器发送
`PUT /proxies/<selector>`，再要求内核清理旧连接。API 暂时失败时仍保留上次
成功读取的菜单数据。

启用 `selector-persist = 1` 后，选择结果保存在
`sing-box-drover.state.json`。只有保存值仍存在于该选择器当前的 `all` 列表时
才会恢复，否则以 API 当前的 `now` 为准。

普通单击托盘图标切换系统代理，按住 Shift 单击切换 TUN。TUN、任务计划程序
开机启动和其他特权操作仅在需要时请求提权。子内核无控制台窗口，受 Windows
Job Object 管理；程序会监控异常退出，并在强制终止前先尝试用 Ctrl+C 正常
关闭内核。
