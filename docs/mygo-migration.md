# mygo 桌面迁移记录

使用 `github.com/egoist/mygo v0.4.8` 共享 Windows、macOS、Linux 的托盘、通知、Shell 和应用生命周期，并使用同一份菜单模型。Windows 弹出菜单用小型 Win32 适配层渲染国旗位图；macOS/Linux 菜单直接交给 mygo。sing-box 进程控制、Clash API、配置、节点选择和故障恢复继续由 `internal/app` 管理。

## 迁移范围

- 删除独立 Win32 托盘和 `fyne.io/systray` 实现，使用一个 `internal/tray/tray.go`。
- 保留系统代理、TUN、Selector/provider、URLTest 落点、Homepage、自启动、重启、状态图标和退出时清理。
- 控制器动作串行执行，阻塞操作移出 GUI 主循环；菜单与图标更新交给 mygo 主线程。通知独立发送，退出不等待通知授权。
- 常规退出等待当前动作结束后关闭控制器；停止内核或恢复代理失败时保留托盘、记录错误，下一次 Quit 重试。内核停止无法确认时保留单实例锁；内核已停止但代理恢复失败时可以释放锁，托盘仍保留以供恢复重试。Windows 原生菜单在此状态下只允许 Quit。
- 确认系统终止的 `OnQuit` 回调直接关闭控制器，不等待可能依赖主线程的工作协程；已成功的清理不重复执行，并销毁原生托盘及临时图标。
- 菜单重建同时考虑 Selector 结构和 TUN 入站变化；队列满时恢复真实勾选状态并提示；无效刷新内容保留旧菜单，并对相同错误只通知一次。
- Windows 保留单击代理、Shift 单击 TUN。macOS/Linux 使用托盘菜单，睡眠恢复使用 mygo Power。
- Linux 接入 `flock` 单实例、重启交接、XDG 自启动、GNOME GSettings 代理和 sing-box 的 TUN 权限检查。GNOME 代理逐项读回校验，写入失败会回滚并验证恢复结果，外部修改后不覆盖用户设置；部分恢复失败后，重试仅写入尚未恢复的项。
- Windows 菜单位图测试适配共享菜单模型，保留国旗像素、勾选列和资源清理检查；其他旧平台测试迁移为共享菜单、图标和生命周期测试；新增 Linux 代理失败回滚及权限检查回归验证。

平台代码仍放在 `internal/windows` 以保留现有导入路径，目录中按构建标签区分三个系统。Windows 原生菜单字体不能可靠显示 Unicode 国旗，因此恢复内置国旗位图：适配层在 mygo 主线程读取共享菜单项，保留原始节点值、勾选状态和点击回调，弹出菜单关闭或构建失败时释放菜单与位图。mygo 托盘在 Windows 不再自动弹出文本菜单，右键回调显示上述原生菜单。macOS/Linux 继续由系统字体显示 Unicode 国旗。macOS 最低版本调整为 13.0。

## main 分支问题修复

已同步至本地 main `6fcd07d` 的适用修复，并接入 mygo 生命周期：

- Windows 提权使用可跟踪的新进程和经过对端 PID/随机令牌验证的命名管道。替换进程先验证实际 INI、配置和内核；旧内核确认停止后才转交单实例锁；新原生托盘与菜单创建成功后再确认接管。失败时仅在确认替换进程退出后恢复旧内核和代理。
- 父实例负责显示已收到的子实例启动失败；子实例仅在报告确实送达时抑制重复提示。mygo 初始化、菜单和确认失败也接入该路径。
- JSON 规范化保留空白与注释两侧的分隔，拒绝开头和连续逗号；取消恢复会结束配置检查子进程；停止结果包含子进程退出和状态通知完成。交接取消和启动超时传到实际取锁重试及内核启动；原生托盘确认前父实例断连仍视为失败。
- 自启查询保留禁用状态；macOS plist 查询校验标签、启动参数及启用条件，取消下次登录启动不结束当前进程；恢复代理时检查认证状态。Windows 构建脚本兼容 PowerShell 5.1，INI 支持文件开头的 UTF-8 BOM。
- 交接测试注入可释放的假实例锁，不使用真实用户的单实例锁；停止失败后重试和主线程阻塞时的强制退出由共享托盘测试验证。

Linux 的 TUN 权限仍允许 sing-box 的必要 capabilities，并未改成只接受 root。命名管道交接用于 Windows；macOS/Linux 保留原平台重启路径。

Windows 实测应将程序、INI、sing-box.exe 与配置所在的整套目录复制到本地 Windows 路径，例如 `C:\Apps\sing-box-drover`，再退出旧实例并启动新程序。映射盘 `Z:` 属于当前登录会话，UAC 提权后的进程可能无法访问；整套文件应从本地目录运行。这是需排查的路径限制，当前没有 Windows 故障日志可证明它是用户 TUN 故障的原因。

## 工具链与 Go #81956

mygo v0.4.8 要求 Go 1.27.1。该版本标准库仍在 `crypto/internal/fips140/drbg/entropy_fips140.go` 中静态声明 32 MiB 的 `entropy.ScratchBuffer`。

[golang/go#81956](https://github.com/golang/go/issues/81956) 记录了 Windows 私有提交内存增加的问题。这不等于固定增加同样的常驻工作集，也不是持续泄漏；单独设置 `GOFIPS140=off` 或 `GODEBUG=fips140=off` 不会移除静态缓冲区。

该缓冲区按 `//go:build !wasm` 编译，不限于 Windows：本工作区实测 Linux amd64 的 `.noptrbss` 为 `0x2020320`（32.1 MiB），macOS arm64/amd64 的 `__noptrbss` 为 `0x200ea98`/`0x200e8a0`（各约 32.06 MiB）。Linux 与 macOS 按需分页，未触碰时不占常驻内存（强制初始化加密后该 VMA 仍只有 72 kB Rss），因此三端都应用同一 overlay，使行为一致并消除低内存环境下 `exec` 即被 SIGKILL 的风险（见 [golang/go#81505](https://github.com/golang/go/issues/81505)，该 issue 仍为 OPEN）。

`tools/go-overlay/main.go` 使用 issue 中的 `sync.OnceValue` 分配方式生成临时 overlay，保留一次分配、后续复用及原有随机数实现。它只接受 Go 1.27.1 及已核对的源码 SHA-256：

```text
bd3c834a29e31c93e56d81da54d4a874561814a86550de1456c8d6d40a9c5fda
```

版本或源码变化会停止构建。原始 GOROOT 不会被修改，生成文件与绝对路径映射仅保存在忽略的 `output/.go-overlay/` 中。

`scripts/build.ps1` 设置并恢复构建环境，固定 `GOFIPS140=off`，自动生成 overlay 后编译。生成器同时检查进程环境和有效的 `go env GOFIPS140`，拒绝启用 FIPS 的配置。用户运行发布程序时也应保持 FIPS 关闭；该临时改动不声称满足 FIPS 验证要求。

`scripts/go-overlay.sh` 为两个 shell 构建脚本提供相同的准备步骤：解析工具链、生成 overlay，并导出 `GO_BINARY`、`GO_OVERLAY_FILE` 与 `GOFLAGS`。overlay 的键必须与构建使用的 GOROOT 一致，否则 Go 会静默忽略替换并产出 32 MiB 二进制；该脚本在生成后校验键是否指向当前 GOROOT，不匹配即失败。

Go 禁止 overlay 替换模块缓存中的文件，包括自动下载的工具链。脚本发现这种情况时，把工具链复制到 `output/.toolchains/go1.27.1`，使用副本生成 overlay 和构建，并保留原缓存。

升级 Go 时先检查 issue 状态和标准库实现，再更新或移除 overlay，不能只改版本号或跳过源码校验。

## 构建与 CI

构建脚本和 CI 固定 Go 1.27.1，三端均设置 `CGO_ENABLED=0`。Linux 运行时仍需 GTK 3 和 AppIndicator 库；macOS 的 Universal 打包与 ad-hoc 签名需要在 macOS 上执行。

- `scripts/build.ps1`：Windows amd64，图标、manifest 和内存 overlay。
- `scripts/build-macos.sh`：macOS arm64/amd64 Universal `.app`，部署目标 13.0，含内存 overlay。
- `scripts/build-linux.sh`：Linux amd64 或 arm64，二进制与 INI，含内存 overlay。
- `scripts/go-overlay.sh`：三端构建共用的 overlay 准备逻辑。
- `.github/workflows/ci.yml`：Windows、macOS、Linux 的格式、测试、vet 和构建检查。
- `.github/workflows/release.yml`：Windows ZIP、macOS Universal ZIP、Linux amd64/arm64 tar.gz 及 SHA-256。

## 本地验证结果

本次在 Linux amd64 工作区执行：

- Go 1.27.1 全量 `go test -count=1 ./...` 与 `go vet ./...` 通过。
- 托盘、应用和 Linux 平台层 `go test -race -count=1 ./internal/tray ./internal/app ./internal/windows` 通过。
- Windows amd64、macOS arm64 的 app/core/tray/platform 测试包编译通过；未在对应系统执行。
- overlay 生成器拒绝临时 `GOENV` 中持久化的 `GOFIPS140=latest`，未生成补丁文件。
- Windows amd64 overlay 构建通过，包含应用图标和 manifest。
- Windows 国旗修复后，Linux 全量测试及 vet、Windows amd64 vet、Windows amd64/macOS arm64 托盘测试包编译通过；原生国旗、勾选和资源清理测试尚未在 Windows 执行。生成 `output/mygo/windows-amd64/sing-box-dover-go-flags.exe`，其 `.data` VirtualSize 为 `0xED410`（971,792 字节），RawDataSize 为 612,352 字节，继续应用 Go 内存 overlay。Windows 虚拟机中的国旗显示仍待确认。
- macOS arm64、amd64 交叉编译通过，两份 Mach-O 的 `LC_BUILD_VERSION` 均为 `minos 13.0`。
- Linux amd64、arm64 通过 `scripts/build-linux.sh` 构建。
- Bash 构建脚本语法检查、CI/发布 YAML 解析、gofmt 和 `git diff --check` 通过。
- 使用隔离的 `GSETTINGS_BACKEND=memory` 确认命令成功但设置不持久化的情况；回归测试覆盖读回不一致、写入失败回滚、部分恢复失败后重试和原生终止时的主线程等待问题。
- Linux amd64 在独立 D-Bus/Xvfb 中使用受控 StatusNotifier 宿主完成运行检查：托盘注册且状态为 Active；两次 SIGTERM 正常退出、停止受控内核并清理临时图标；同一路径再次启动成功，单实例锁已释放。代理和 TUN 均关闭，证据保存在 `output/mygo/runtime-smoke/evidence.json`。

恢复国旗前的迁移构建中，相同源码、工具链、资源及链接参数下，Windows PE `.data` 比较为：

| 构建 | VirtualSize | RawDataSize |
| --- | --- | --- |
| 原始 Go 1.27.1 | `0x20B6320`（34,300,704 字节） | 387,072 字节 |
| Go 1.27.1 + overlay | `0xB6310`（746,256 字节） | 387,072 字节 |

VirtualSize 减少 33,554,448 字节，约 32 MiB；磁盘原始数据大小相同，所以不能通过 EXE 文件大小判断此问题是否消失。

三端 overlay 接入后，本工作区实测静态缓冲区大小：

| 平台 | 未打补丁 | 应用 overlay |
| --- | --- | --- |
| Linux amd64 `.noptrbss` | `0x2020320`（32.1 MiB） | `0x020320`（约 128 KiB） |
| macOS arm64 `__noptrbss` | `0x200ea98`（32.06 MiB） | `0x000ea88`（约 58 KiB） |
| macOS amd64 `__noptrbss` | `0x200e8a0`（32.06 MiB） | `0x000e890`（约 58 KiB） |
| Windows amd64 `.data` VirtualSize | `0x20B6320`（32.7 MiB） | `0xB6310`（约 0.71 MiB） |

`scripts/build-linux.sh` 已在本机使用 Go 1.27.1 完整执行成功，产出的 Linux amd64 二进制 `.noptrbss` 为 `0x020320`。macOS 交叉编译的 arm64/amd64 产物均确认 overlay 生效；`scripts/build-macos.sh` 的 Universal 打包与签名仍需在 macOS 上执行。

上述结果验证编译、共享逻辑、Linux 托盘生命周期和静态数据布局。Windows 私有提交内存、三端真实桌面菜单交互、自启动、提权、GNOME 代理、TUN、休眠恢复及 macOS 签名打包仍需对应系统实机验证。GitHub Actions 尚未在远端执行，未创建发布或标签。
