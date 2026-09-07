## 目录结构

- `cmd/sing-box-drover`：程序入口；
- `internal/app`：应用编排；
- `internal/config`：INI、JSON、TUN 过滤和 BPF 配置读取；
- `internal/clash`：选择器读取、切换和状态持久化；
- `internal/core`：所管理的 `sing-box.exe` 生命周期；
- `internal/tray`：原生 Win32 托盘和选择器菜单渲染；
- `internal/windows`：系统代理、提权、单实例和开机启动；
- `docs`：用户说明和开发资料；
- `tools`：国旗资源生成器等离线开发工具；
- `examples`：纳入版本控制的示例配置；
- `output`：不纳入版本控制的构建与本地运行目录。

## 本地构建

托盘控制器是原生 Windows 程序，不内置 sing-box 内核：

```powershell
$env:GOTOOLCHAIN = "go1.25.6"
go test ./...
go vet ./...
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -trimpath -ldflags="-s -w -H=windowsgui" -o .\output\sing-box-dover-go.exe .\cmd\sing-box-drover
```

`go.mod` 暂时将 Go 1.25.6 固定为低内存构建基线。`toolchain` 指令不会让
已经运行的 Go 1.27 自动降级，因此需要在 PowerShell 中设置
`$env:GOTOOLCHAIN = "go1.25.6"`。

## Go 版本和内存

本地复现表明，Go 1.26 和 Go 1.27 在 `net/http` 等 TLS 路径中会引入
32 MiB 的 FIPS 140 熵源缓冲区（`crypto/internal/fips140/drbg.memory`）。
即使未启用 FIPS，该缓冲区在 Windows 上也会成为驻留私有内存，使托盘进程
额外占用约 34 MB。上游问题 [`golang/go#78321`](https://github.com/golang/go/issues/78321)
记录了 Wasm 场景；Windows 工作集结论来自本地复现。在确认新版工具链修复前，
不要把 Go 1.27 当成解决方案。

## 自动发布

推送以 `v` 开头的版本标签（例如 `v0.1.0`）后，`.github/workflows/release.yml`
会自动运行格式检查、测试、静态检查和 Windows amd64 构建，并创建对应的
GitHub Release。

发布包名称为 `sing-box-dover-go-v版本-windows-amd64.zip`，压缩包内包含
`sing-box-dover-go.exe` 和兼容旧安装的 `sing-box-drover.ini`，不包含
`sing-box.exe` 内核。Release 同时提供 ZIP 的 SHA256 校验文件。

发布示例：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

## 提交前检查

```powershell
gofmt -l cmd internal tools
go test -count=1 ./...
go vet ./...
```
