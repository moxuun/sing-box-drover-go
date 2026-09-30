# 使用说明

## 运行目录

控制器不内置代理内核。运行时需要准备：

- 控制器程序；
- 对应平台的 sing-box 内核：Windows 为 `sing-box.exe`，macOS 为 `sing-box`；
- 从 `examples/sing-box-drover.ini` 复制并改名得到的 `sing-box-drover.ini`；
- 真实的 `config.json`。

Windows 发布包是一个目录，直接放入 `sing-box.exe`、`sing-box-drover.ini` 和
`config.json` 后运行 `sing-box-dover-go.exe`。

macOS 发布包同时提供 arm64 和 x86_64：

```text
sing-box-drover-v版本/
├── sing-box-drover.app
└── sing-box-drover.ini
```

请把 `sing-box` 和 `config.json` 放在 `.app` 旁边，然后双击
`sing-box-drover.app`。未经过 Apple 公证的本地发布包如被 Gatekeeper 拦截，
可右键选择“打开”，或按实际安装位置移除隔离属性：

```bash
xattr -dr com.apple.quarantine /path/to/sing-box-drover.app
```

也可以通过 `sb-dir` 和 `sb-config-file` 指向其他位置。控制器通过标准输入把
运行时 JSON 交给内核，切换 TUN 或系统代理时不会改写源配置。

托盘里的 `Homepage` 会打开 `sing-box-drover.ini` 中的 `homepage-url`。该项
接受 `http://` 或 `https://` 地址，例如可直接填写本地 Web 面板
`http://127.0.0.1:9090/ui/`。

## 首次测试

准备一个干净的测试目录，放入控制器、目标内核和真实 JSON 配置。首次启动前，
建议在 `sing-box-drover.ini` 中临时使用：

```ini
system-proxy-auto = off
log-file = sing-box-drover.log
```

启动前应完整退出旧 Drover，并停止会占用相同 mixed、TUN 或 Clash API 端口的
其他 `sing-box`、reF1nd 或兼容内核。先关闭 TUN 启动并查看日志，再依次测试
系统代理、打开选择器菜单、切换节点，最后测试需要提权的 TUN 和登录启动。
确认无误后，如果需要启动时自动开启系统代理，再把 `system-proxy-auto` 改为
`on`；手动开启的系统代理在控制器退出时也会恢复开启前的用户设置。

## 系统代理和 TUN

Windows 使用 WinINet 的系统代理设置。macOS 使用 `networksetup` 修改当前主
网络服务的 HTTP、HTTPS 和 SOCKS 代理，并在退出或重启内核时恢复原设置。若
系统代理在 Drover 运行期间被用户或其他程序修改，控制器会放弃覆盖，保留外部
的新设置。

macOS 的 `networksetup` 写操作和 TUN 都需要管理员权限。Windows 会请求 UAC；
macOS 会显示管理员授权对话框。若先在普通权限下开启系统代理或 TUN，控制器会
交接到提权后的新实例；启动时设置 `system-proxy-auto = on` 也会触发同样的授权。
macOS 以管理员权限运行时无法为当前普通用户写入 LaunchAgent，因此请先在普通
权限下设置 “Start with macOS”，再开启系统代理或 TUN。

## 选择器和节点记忆

内核提供 `experimental.clash_api` 时，状态栏菜单会请求 `GET /proxies`。
所有 `type: "Selector"` 项按 API 返回顺序显示，其 `all` 列表保持原样，包括
provider 展开的节点；当前 `now` 项会被勾选。选择节点后，控制器发送
`PUT /proxies/<selector>`，再要求内核清理旧连接。API 暂时失败时仍保留上次
成功读取的菜单数据。

如果当前选项指向 `URLTest` 自动选择组，菜单会在勾选项后显示内核当前选中的
节点；该节点只用于显示，不提供单独的手动修改入口。

选择器是否记忆由 sing-box 自身配置决定；控制器不会额外写入独立的状态文件，
也不会修改源配置。旧配置中的 `selector-persist` 项会被忽略。

## 登录启动

Windows 的 “Start with Windows” 使用任务计划程序中的 `sing-box-drover`
任务，不是“设置 → 应用 → 启动”里的注册表启动项。可在任务计划程序根目录检查，
或运行：

```powershell
schtasks /Query /TN sing-box-drover /FO LIST /V
```

macOS 的 “Start with macOS” 使用：

```text
~/Library/LaunchAgents/com.moxuun.sing-box-drover.plist
```

关闭该选项会移除这个 plist，并尝试卸载当前会话中的 LaunchAgent。若随后需要
开启系统代理或 TUN，请在普通权限实例中完成登录启动设置；提权后的实例只能管理
当前会话，不能替普通用户修改 LaunchAgent。

## 其他细节

- Windows 支持 `Shift + 左键` 点击托盘图标快速开启/关闭 TUN；macOS 使用状态栏
  菜单中的 TUN 项。
- 托盘图标绿色/红色/无色，分别代表代理运行中/出现错误/代理关闭。
- macOS 会监听系统唤醒通知，并在内核或 Clash API 异常时自动恢复。
- 如果日志出现 `listen tcp ... bind`，先释放对应端口或修改源配置中的 Web/API
  端口；控制器不会擅自改写用户的 sing-box 配置。
