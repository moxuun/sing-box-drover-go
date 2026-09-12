## 本地运行

本地运行文件统一放在 `output`。将 `sing-box.exe`、从
`examples/sing-box-drover.ini` 复制并改名得到的 `sing-box-drover.ini`，以及
`config.json` 放在控制器旁边。也可以通过 `sb-dir` 和
`sb-config-file` 指向其他位置。控制器通过标准输入把运行时 JSON 交给内核，
切换 TUN 或系统代理时不会改写源配置。

托盘里的 `Homepage` 会打开 `sing-box-drover.ini` 中的 `homepage-url`。该项
接受 `http://` 或 `https://` 地址，例如可直接填写本地 Web 面板
`http://127.0.0.1:9090/ui/`。

## 首次在 Windows 上测试

准备一个干净的测试目录，放入控制器、目标内核和真实 JSON 配置。首次
启动前，建议在 `sing-box-drover.ini` 中临时使用：

```ini
system-proxy-auto = off
log-file = sing-box-drover.log
```

启动前应完整退出旧 Drover，并停止会占用相同 mixed、TUN 或 Clash API 端口
的其他 `sing-box.exe`、reF1nd 或兼容内核。先关闭 TUN 启动并查看日志，再依次
测试系统代理、打开选择器菜单、切换节点，最后测试需要提权的 TUN 和任务计划
程序。确认无误后，如果需要启动时自动开启、退出时自动清理系统代理，再把
`system-proxy-auto` 改为 `on`。

## 选择器和节点记忆

内核提供 `experimental.clash_api` 时，托盘打开菜单会请求 `GET /proxies`。
所有 `type: "Selector"` 项按 API 返回顺序显示，其 `all` 列表保持原样，包括
provider 展开的节点；当前 `now` 项会被勾选。选择节点后，控制器发送
`PUT /proxies/<selector>`，再要求内核清理旧连接。API 暂时失败时仍保留上次
成功读取的菜单数据。

如果当前选项指向 `URLTest` 自动选择组，菜单会在勾选项后显示内核当前选中的节点；
该节点只用于显示，不提供单独的手动修改入口。

选择器是否记忆由 sing-box 自身配置决定；控制器不会额外写入独立的状态文件，
也不会修改源配置。旧配置中的 `selector-persist` 项会被忽略。

普通单击托盘图标切换系统代理，按住 Shift 单击切换 TUN。TUN、任务计划程序
开机启动和其他特权操作仅在需要时请求提权。子内核无控制台窗口，受 Windows
Job Object 管理；程序会监控异常退出，并在强制终止前先尝试用 Ctrl+C 正常
关闭内核。

“Start with Windows” 使用任务计划程序中的 `sing-box-drover` 任务，不是“设置 → 应用 →
启动”里的注册表启动项，因此不会出现在那个列表中。可在任务计划程序根目录检查它，或运行
`schtasks /Query /TN sing-box-drover /FO LIST /V`。

如果日志出现 `listen tcp ... bind`，先释放对应端口或修改源配置中的 Web/API 端口；控制器不会
擅自改写用户的 sing-box 配置。

## 其他细节
- `shift + 左键` 点击托盘可以快速开启/关闭 TUN 模式。
- 托盘图标绿色/红色/无色，分别代表代理运行中/出现错误/代理关闭。
