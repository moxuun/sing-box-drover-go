# 代码审查记录

审查日期：2026-09-07。审查对象为当前 Go 实现，并对照了
`hdrover/sing-box-drover` 可观察到的行为。

## 尚未修复的问题

### 高：异常退出码为 0 时会被当成正常停止

`internal/core.Supervisor.wait` 只有在 `cmd.Wait` 返回错误时才报告失败。如果
内核并未收到停止请求，却以退出码 0 结束，监督器会发布 `StateStopped`。
`internal/app.App.handleCoreEvent` 因此不会执行内核失败后的系统代理清理，可能
让 Windows 继续指向已经停止的内核。

建议：只要没有设置 `stopRequested`，无论退出码是多少都应视为异常退出，并
增加退出码 0 的回归测试。

### 中：打开托盘菜单会在界面线程同步请求 API

`internal/tray.Tray.showMenuAt` 在 Win32 消息线程中调用 `RefreshSelectors`。
生产环境的 Clash 客户端超时为 1 秒，因此 API 停止或无响应时，右键菜单可能
整整 1 秒看起来毫无反应。

建议：立即用上一次成功的数据绘制菜单，在后台刷新供下次打开使用；或者为
菜单路径设置更短的请求期限。

### 中：Restart 没有同步调整 BPF 更新器生命周期

Restart 会重新读取 JSON/BPF 源并替换内核与 API 状态，但 BPF 更新器只在应用
首次启动时创建。把配置从 JSON 改为自动更新的远程 BPF 后不会启动更新器；
关闭或修改已有更新器，也要等旧定时器再次触发后才会被发现。

建议：新配置成功启用时，根据候选配置停止并重建更新器，并覆盖 JSON 转 BPF、
BPF 转 JSON 两种回归测试。

### 低：选择器位图归一化失败后仍会继续使用

即使 `normalizeNativeCheck` 失败，`createSelectorBitmap` 仍会返回并挂载该位图。
这可能把黑白遮罩显示成白色方块，而不是退回普通原生勾选。

建议：把归一化失败视为绘制失败，删除位图，让 `appendSelectorItem` 使用已有的
原生勾选回退路径。

## 已执行的验证

- Go 1.25.6：`go test -count=1 ./...` 通过；
- Go 1.25.6：`go vet ./...` 通过；
- 当前 Windows 环境未启用 CGO，无法运行竞态检测；Win32 生命周期和界面线程
  行为仍需真实托盘测试。
