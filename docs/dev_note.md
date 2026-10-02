# 开发说明

## 功能点与改进记录

状态说明：`已完成` 表示代码和自动检查已具备；`待实机验证` 表示还需要在真实 Windows/macOS 托盘、提权、睡眠唤醒或网络环境中确认；`设计待决定` 表示已经审查出问题，但不代表已经修改。

### 协作规则

- `已完成` 每个功能开发、修复、审查、讨论和验证任务结束前同步本文档；同一功能更新原条目，不重复记录。
- `已完成` 本文件只记维护边界、实现改进、验证结论和未决事项；用户操作说明放用户文档。

### 核心功能

- `已完成` 轻量跨平台托盘控制器，使用用户自带的外置 `sing-box`/`sing-box.exe` 和原生配置；不内置内核，不做订阅管理、配置编辑和流量统计。
- `已完成` 托盘负责单实例、内核启停重启、状态显示、Homepage 和系统集成。Windows 用原生 Win32 消息循环和通知区域图标；macOS 用 `fyne.io/systray` 的原生 Objective-C/AppKit 状态栏，不引入 Electron、WebView 或大型 UI 框架。
- `已完成` 通过运行中内核的 Clash API 读取 Selector，用 `PUT /proxies/<selector>` 切换节点；provider 展开的节点、国旗和当前选择显示在托盘菜单。
- `已完成` 静态 Selector 缓存遵循 sing-box 的默认值：配置省略 `default` 时勾选 `outbounds` 第一项，显式默认值原样保留。
- `已完成` Selector 刷新和切换尊重调用方上下文取消；取消后返回的过期响应不写入缓存，nil context 归一化为后台上下文。
- `待实机验证` 读取运行中 `URLTest.now`，在自动选择组的当前选项后显示实际落点；落点只用于显示，不新增手动切换入口。
- `已完成` Selector 菜单保留 Windows 原生勾选样式，并处理勾选标记与节点旗帜重叠；菜单布局不再扩展。
- `已完成` Homepage 支持配置 `http://` 或 `https://` 地址，未配置时指向本项目仓库。
- `已完成` 单击托盘切换 Windows 系统代理，Shift 单击切换 TUN。
- `已完成` TUN 支持按配置启动；普通权限进程需要 TUN 时经 UAC 提权并交接给新的托盘实例。
- `待实机验证` 自启使用固定任务名 `sing-box-drover`；同一 Windows 用户下 A、B 版本启用自启时后一次以 `/F` 覆盖，开机只会启动后一次设置的版本。
- `已完成` 自启任务用 `/RL LIMITED` 以当前用户权限启动，需要 TUN 时仍走按需 UAC；改任务由提权后的替代进程完成，非 TUN 场景不长期运行管理员托盘。
- `已完成` 自启更新改为一次性提权助手：`-autostart-enable`/`-autostart-disable` 在 `NewAt` 里、取单实例和读配置之前处理，返回 `ErrAutostartHandled`，不接管控制器、不启动内核、不建托盘就退出；`LaunchAutostartElevated` 不再传 `-restart`、不带 TUN 状态、不做系统代理交接（助手不启动内核，交出去没人恢复代理）。附带变化：自启更新不再做配置预检。
- `待实机验证` 普通权限勾选「Start with Windows」，UAC 通过后确认原托盘继续运行且任务管理器中仍是非管理员、任务注册成功；拒绝 UAC 时原托盘保留并提示。
- `已完成` 自启任务的账户身份取自发起操作的账户：托盘提权前用 `-autostart-owner` 把账户名传给助手，助手据此调用 `SetAutostart(enabled, owner)`，为空时才回落 `user.Current()`；跨账户 UAC 不再把任务注册给凭据账户，并拒绝含引号或换行的用户名。
- `待实机验证` 跨账户 UAC 需要第二个账户，本机只有一个，无法复现。附带实测：非提权会话下 `schtasks /Create` 直接返回「拒绝访问」（不带 `/RU` 也一样），所以提权助手必须保持提权运行。
- `设计待决定` 固定任务名是机器级资源、未按用户隔离，不同 Windows 用户各自启用会互相覆盖或删除。迁移陷阱：改成按用户命名后，旧任务名的注册既不被 `QueryAutostart` 看到、也不被 `SetAutostart(false)` 删除，会出现「关不掉」和登录时重复注册；需在「读任务 XML 的 `<Principal><UserId>` 接管旧任务」与「保留固定名字但拒绝修改其他用户的任务」之间选择。
- `已完成` Task Scheduler 的查询、创建和删除使用 10 秒超时。
- `已完成` Task Scheduler 的 `/XML` 输出支持原始 UTF-16（含或不含 BOM）以及字节流已转换但声明仍为 `UTF-16` 的输出，自启验证不再因 `Decoder.CharsetReader is nil` 失败。
- `已完成` 自启状态查任务 XML 的 `Settings/Enabled`，任务存在但被禁用时不再误报为已启用；关闭自启始终执行幂等删除。
- `待实机验证` 确认真实登录后任务以普通权限启动、TUN 场景只在需要时弹出 UAC，以及已有旧的 `HIGHEST` 任务能被更新为 `LIMITED`。
- `已完成` macOS 状态栏菜单支持系统代理、TUN、Selector、重启、Homepage、登录启动和退出，并定期同步 Clash API 首次展开的 provider 节点。
- `已完成` macOS 系统代理用 `networksetup` 修改当前默认网络服务的 HTTP、HTTPS、SOCKS、PAC、自动发现和绕过列表；普通权限实例按需交接管理员实例，外部修改后放弃覆盖，比较时忽略关闭状态的端点残留。
- `已完成` macOS 单实例使用 `flock`，提权交接把原用户锁文件路径传给 root 实例。
- `已完成` macOS 代理和 TUN 交接共用 `osascript ... with administrator privileges`；普通启动使用独立 session，避免终端退出中断控制器。
- `已完成` macOS 登录启动写 `~/Library/LaunchAgents/com.moxuun.sing-box-drover.plist`；管理员实例拒绝写 root 的 LaunchAgent。
- `已完成` macOS 监听 `NSWorkspaceDidWakeNotification`，唤醒后复用 `RecoverAfterResume` 检查内核与 Clash API。
- `已完成` macOS `.app` 用 `LSUIElement` 隐藏 Dock 图标；默认构建 arm64 + x86_64 Universal，最低系统版本固定 macOS 12.0，删中间文件后执行 ad-hoc 签名。
- `已完成` 2026-09-30 在 macOS 27.0.1 arm64 上用 sing-box 1.13.4 完成普通权限启动验证（验证时关闭系统代理和 TUN）。
- `待实机验证` 真实 macOS 用户会话中确认系统代理授权、LaunchAgent 登录启动、管理员 TUN 托盘、睡眠唤醒和真实配置的完整生命周期。

### 内核与配置边界

- `已完成` 读原始 sing-box JSON，经标准输入把运行时 JSON 交给内核，不改写用户配置文件。
- `已完成` TUN 开关只生成内存中的带 TUN/不带 TUN 变体；配置、DNS、路由和出站规则仍由用户配置决定。
- `已完成` 预检拒绝超出 TCP 端口范围的 `mixed.listen_port`。
- `已完成` 预检和 Windows 系统代理入口拒绝纯空白或含 NUL 的 `mixed.listen`。
- `已完成` JSON-with-comments 拒绝未闭合块注释、连续逗号和根对象外的尾逗号；对象或数组内的单个尾逗号仍兼容。
- `已完成` 已移除旧的 BPF 工作流和控制器自有的 Selector 状态文件，节点记忆交给 sing-box 和运行中的 Clash API。
- `已完成` INI 布尔选项统一 `on/off`，兼容 `0/1`，未知值报明确错误。
- `设计待决定` 有 Selector 但没有 Clash API 时仍会注入 `127.0.0.1:9090` 和随机 secret；是否删除这类隐式修改由所有者决定。
- `设计待决定` 目标是「裸核 + 原生配置决定代理行为，托盘只做 Windows 集成和 Clash API 控制」；新功能必须先证明属于这个边界。

### 生命周期与故障处理

- `已完成` `WM_QUERYENDSESSION` 只返回 TRUE，不关闭控制器；只有收到 `WM_ENDSESSION` 且 `wParam != 0` 才关闭。取消关机或注销后托盘、内核和系统代理都可用。
- `待实机验证` 发起注销或关机后取消，确认托盘仍能切换代理、重启内核和退出。
- `已完成` 修掉启动 750ms 计时器与退出协程的竞态：状态发布统一在 `publishMu` 下完成，`promoteToRunning` 只在代次未变、进程仍在、状态仍为 Starting 时发布 Running；已退出的内核不会被标记为运行中，也就不会为它开启系统代理。
- `已完成` 该竞态已用真机复现台确认（假 `sing-box.exe` 驱动真实 `core.Supervisor`，扫 750ms 宽限边界）：真实窗口只有亚微秒级，未改动代码扫 144 次 0 违规；把窗口人为拉宽到 10ms 后，修复前 22/168 违规（`starting -> failed -> running` 且 `Start` 返回 nil），修复后 0/168。复现台在临时目录，未进入仓库。
- `待实机验证` 让内核在宽限期内失败（例如配置错误），确认真实托盘落到失败/停止而不是运行中，且系统代理保持关闭。
- `已完成` 替换实例的等待预算由 `core.StopGracePeriod` + `core.StopCleanupWait` + 5 秒余量导出，覆盖旧实例的完整关闭预算；交接超时返回 `ErrRestartHandoff` 提示用户重新启动，不再静默退出。
- `待实机验证` 旧实例需要走满优雅停止窗口时点「重启内核」，确认新实例能完成交接而不是两边都退出。
- `已完成` 交接总是传递显式的功能状态：`-no-tun`/`-no-proxy` 与 `-tun`/`-proxy` 成对，三个交接调用点统一用 `handoffTunFlags`/`handoffProxyFlags`，`startupTunRequested`/`startupProxyRequested` 让显式关闭优先于 INI 的 `tun-start-mode`/`system-proxy-auto`；托盘的代理启动条件改问 `App.StartupProxyRequested()`。
- `待实机验证` INI 里 `tun-start-mode`/`system-proxy-auto` 为 on、手动关掉后点「重启内核」，确认重启后仍是关闭状态；提权交接后同样保持。
- `已完成` 睡眠恢复不再把 Clash 客户端自身的 1 秒请求超时当成恢复上下文结束（该错误包裹 `context.DeadlineExceeded`）；`resumeProbeEndedRecovery` 改为只判断调用方 `ctx.Err()`，连续 API 超时后仍会重启失效内核。
- `待实机验证` 唤醒后内核 API 持续无响应（内核假死）时，确认控制器最终重启内核并恢复选择器，而不是直接返回错误。
- `已完成` 内核由控制器拥有并监控，启动时收集有上限的标准输出/错误输出，异常退出时报告 `FATAL` 等诊断信息。
- `已完成` 停止内核前先尝试优雅关闭，用 Windows Job Object 等机制减少孤儿进程；优雅关闭信号发送失败时不再白等 10 秒，立即走强制清理。内核失败或被外部正常停止时清理控制器开启的系统代理状态。
- `已完成` 重启、TUN 切换和睡眠恢复重启前重读配置，先在控制器侧检查 JSON 和必要字段，再交给外置内核执行 `sing-box check -c stdin`；失败时保留当前配置状态和运行中的旧内核。
- `已完成` 同进程重启或 TUN 切换会先恢复由控制器接管的系统代理，内核起来后再按当前配置重新启用，不让代理在旧内核停止期间指向已停止的进程。
- `已完成` 睡眠恢复的 Clash API 探测在请求前后检查取消状态，取消后仍返回成功也不会继续执行恢复重启。
- `已完成` 受保护实机测试已用当前外置 `sing-box.exe` 验证原生检查接受合法配置、拒绝 JSON 合法但语义非法的配置，当前真实配置的 TUN/非 TUN 变体都通过。
- `已完成` 进入关闭状态后，选择器切换、TUN 切换、重启和提权交接都拒绝后续操作。
- `待实机验证` 保持旧内核运行时现场写入一份语义错误配置，分别点击「重启内核」和切换 TUN，确认提示清晰且旧 PID、代理连接和选择器状态不受影响。
- `已完成` 重启改为启动新托盘进程并交接互斥体和 TUN 参数，再退出旧进程，缓解多次重启后的堆高水位；Selector 状态仍由运行中 API 和 sing-box 缓存恢复。
- `待实机验证` 连续点击「重启内核」，观察 PID、内存、句柄、GDI/USER 资源以及节点/TUN 状态。
- `已完成` 睡眠唤醒后重新注册通知区域图标并检查内核和 Clash API，失效时尝试恢复服务。Running 且 API 曾经就绪时，唤醒后 `/proxies` 最多重试 5 次、间隔 750ms、总超时 10 秒，只有连续失败才进入配置预检和重启路径，重复唤醒只启动一次恢复；收到取消或超时立即结束，不再误判为 API 故障。
- `待实机验证` 真实睡眠/唤醒周期中观察 API 短暂不可用、内核真实退出和托盘提示，确认重试窗口覆盖实际恢复时间且不掩盖真正故障。

### Windows 代理与 Discord 问题

- `已完成` 本地协作规范 `AGENTS.md` 已精简为 BUG 修复与维护规则；代理、退出和重启行为先参考成熟客户端、优先核对官方 sing-box，不为假设场景增加恢复或归属逻辑，也不强制保留原代理保护策略。
- `已完成` 代理策略以官方 Windows 客户端为依据：sing-box 提交 `927770c29f3e3698c711fc150e1066a4a78793a2` 的 `experimental/boxdd/platform_windows.go`、`common/settings/proxy_windows.go`，sing 提交 `6f21f2425a95` 的 `common/wininet/wininet_windows.go`。关闭时 `ClearSystemProxy` 只把 flags 设为 `DIRECT | AUTO_DETECT`，不恢复旧快照、不检查当前设置是否被其他软件改过；服务器、bypass 和 PAC 字符串不清空，手动代理和显式 PAC 标志关闭。
- `已完成` 成熟客户端同样不恢复接管前的快照：v2rayN 的 `AppExitAsync` 走 ForcedClear，Windows `UnsetProxy()` 设置 DIRECT；Clash Verge Rev 的关闭分支和 `reset_sysproxy` 关闭全局代理与 PAC。所以「退出必须恢复旧代理」不是通用正确标准，恢复失效旧端口还可能导致断网。来源：[官方 Desktop 文档](https://sing-box.sagernet.org/clients/desktop/)、[Windows 平台调用](https://github.com/SagerNet/sing-box/blob/927770c29f3e3698c711fc150e1066a4a78793a2/experimental/boxdd/platform_windows.go)、[底层 WinINet 实现](https://github.com/SagerNet/sing/blob/6f21f2425a95/common/wininet/wininet_windows.go)、[v2rayN SysProxyHandler](https://github.com/2dust/v2rayN/blob/master/v2rayN/ServiceLib/Handler/SysProxy/SysProxyHandler.cs)、[AppManager](https://github.com/2dust/v2rayN/blob/master/v2rayN/ServiceLib/Manager/AppManager.cs)、[ProxySettingWindows](https://github.com/2dust/v2rayN/blob/master/v2rayN/ServiceLib/Handler/SysProxy/ProxySettingWindows.cs)、[Clash Verge Rev sysopt](https://github.com/clash-verge-rev/clash-verge-rev/blob/main/src-tauri/src/core/sysopt.rs)。结论仅针对本次读取的分支实现。
- `已完成` 关闭系统代理已按该结论实现：`RestoreSystemProxy` 只写 WinINet 的 flags 一项（`DIRECT | AUTO_DETECT`），不恢复快照、不比较当前值；`restoreTarget`、`proxyHostsMatch`/`proxyAddressMatch`、`proxySettingsEqual` 已删除，`ProxySession` 在 Windows 上不再携带状态（macOS 仍记录旧配置，`networksetup` 没有等价的单项清空）。
- `已完成` 上一条的实机验证（本机注册表往返）：开启后读回 `flags=0x3 server="http://127.0.0.1:10808" bypass="<local>" pac=""`，清空后读回 `flags=0x9` 且三个字符串完全不变，结束后注册表与测试前逐项一致。此前按旧前提写的两次修复已用 `git revert` 撤销（`e94d2a8`、`5006ffb`）。
- `已完成` 有意保留的行为变更：用户自有的其他代理（例如先由其他程序设为 `127.0.0.1:7890`）在本程序开启再关闭后也会被关闭，而不是被恢复。这是官方客户端的既有行为，不要再加回恢复或归属判断逻辑。
- `已完成` 旧系统代理写法曾同时写入 `http`、`https`、`socks` 映射，可能让 Discord 的 WSS 连接走不同的 SOCKS 路径；现已收敛为单一 HTTP 代理并补发 WinInet 代理设置变更通知，mixed 入站的 SOCKS 能力保留。
- `待实机验证` 退出旧托盘、重新启动 Discord 后确认 WSS 连接正常。
- `已完成` 开启前仍会读取现有的标志、服务器、bypass 和 PAC 作为写入基准，本程序不理解的标志和字符串在开启期间保持原值；写入后的校验失败会回滚，并把回滚失败与验证错误一起向上报告。
- `已完成` 手动开启与自动开启共用同一所有权生命周期：退出、内核失败、托盘重启、TUN 提权和开机启动提权都会恢复或交接代理状态，新进程启动失败时原进程重新接管。接管入口要求内核处于 `Running`；退出时把恢复错误传给主流程以便显示诊断。
- `已完成` mixed 入站监听通配地址时不再把系统代理指向不可达地址：`ReadSingBoxConfig` 用 `clientHost` 把 `0.0.0.0`、`::`、`*`、`[::]` 折算成 `127.0.0.1` 并剥掉 `listen` 自带的中括号；指向具体地址的配置（含 `::1`、`localhost`）不受影响。
- `待实机验证` 把配置改成 `"listen": "::"`，开启系统代理后确认能正常上网，且注册表 `ProxyServer` 写的是 `http://127.0.0.1:<port>`。
- `设计待决定` sing-box 自身的 `set_system_proxy: true` 会在内核启停时写入和清除系统代理，与托盘开关构成双重所有权，托盘关闭可能被内核重新改写而表现为「开关无效」；是否检测该字段并在托盘提示用户改为 `false` 尚未决定。
- `待实机验证` 手动代理、PAC/自动检测原状态、退出、连续重启、TUN UAC 交接和用户中途修改代理等完整交互仍需在真实托盘中验证。
- `已完成` 网络问题优先按 Wireshark、Windows 代理注册表和 sing-box 日志这条证据链排查。

### 托盘外观与构建发布

- `已完成` 托盘图标已嵌入正式 Windows 构建，状态区分未运行、代理/TUN 工作中和故障，tooltip 显示当前模式。
- `已完成` 内核进入 `Stopped` 后托盘优先显示「未运行」，不再因保留的 TUN/代理期望状态继续显示绿色工作中；启动和重启过渡态保留原提示。
- `已完成` Homepage 调用检查 URL 编码和 `ShellExecuteW` 返回值，浏览器启动失败会在托盘报错。
- `已完成` 托盘窗口类记录注册所有权，失败或关闭时只注销本进程实际注册的类；关键 Win32 调用归一化空的 `GetLastError`，不再把 `%!w(<nil>)` 暴露给用户。
- `已完成` 托盘菜单遇到无法编码的节点名、分组标题、分隔线或底部操作项时报错并销毁不完整菜单，不再留下失配的命令映射或 GDI 资源。
- `待实机验证` 2026-09-16 观察到跨 S0 Modern Standby 运行时首次打开菜单偶发缺少 Selector 的复合勾选/国旗位图（再次打开自行恢复）；现已改为直接写入 DIB 像素、GDI 勾选绘制后执行 `GdiFlush` 并规范化 alpha，自动测试通过，真实睡眠唤醒循环和 Start11 启用/停用仍待确认。
- `待实机验证` 图标颜色、tooltip 和取消系统代理/TUN 后的状态需要在真实托盘中确认，自动化测试不能替代视觉检查。
- `已完成` 正式构建统一使用 `scripts/build.ps1`（Go 1.25.14、应用图标和 manifest）和 `scripts/build-macos.sh`（CGO、`Info.plist`，默认输出 Universal `.app`）。
- `已完成` GitHub Actions 在推送 `v*` 标签时跑格式检查、测试、`go vet` 并打包产物，由独立任务创建 GitHub Release；该任务不再检出仓库后曾报 `failed to run git: fatal: not a git repository`，现已显式设置 `GH_REPO: ${{ github.repository }}`。
- `已完成` `v0.1.1` 至 `v0.1.5` 的远程发布工作流均已成功，标签构建与 GitHub Release 链路已实际验证。
- `已完成` 持续集成工作流在 Windows 和 macOS runner 上执行格式检查、全量测试和 `go vet`；发布工作流只负责版本标签产物。
- `已完成` `scripts/build.ps1` 会保存并恢复调用者原有的 `GOTOOLCHAIN`、`GOOS`、`GOARCH` 和 `CGO_ENABLED`，并清理临时资源文件。
- `待实机验证` 正式构建仍需在运行中的旧 EXE 场景核对目标替换结果、构建信息和是否遗留 `.exe~`；脚本不会强制结束用户正在运行的程序。
- `已完成` 工具链从 Go 1.25.6 更新到 Go 1.25.14，仍以 Go 1.25 为主版本构建基线；Go 1.26/1.27 不直接升级。
- `已完成` README 的 4–8 MiB 说明已标注为 Go 1.25.6 的历史样本，不再作为当前版本的性能承诺。
- `待实机验证` Go 1.25.14 与旧 1.25.6 的内存和完整托盘生命周期尚未做同一条件对比，结论仍需真实 Windows 复测。
- `待实机验证` 2026-09-12 对当时运行的 Go 1.25.6 产物连续采样约为 30.2 MiB Working Set、19.3 MiB Private Memory、406 handles，与 README 的 4–8 MiB 截图不一致；该产物构建信息指向 `v0.1.5+dirty`，需要用当前提交的正式构建冷启动后按相同口径复测，不能据此认定代码回归或继续沿用旧宣传值。
- `设计待决定` 项目名称目前在仓库/模块/INI 的 `drover` 与二进制/发布包的 `dover` 之间混用；兼容名称和对外品牌仍需单独决定，暂不改名。
- `已完成` README 的 Release 徽章指向本仓库 Releases 页面，Build 徽章指向普通 push/PR 的 `ci.yml`。
- `已完成` 用户文档（`usage_note.md` 与 `README.md`）去人机化重构：精简说教与冗余底层实现科普，聚焦准备文件、快捷操作、配置要点及常见排查；示例 INI 提供中文注释；`AGENTS.md` 只作为本地协作文件，不应上传到仓库。
- `已完成` 2026-09-12 维护审计在固定 Go 1.25.6 下通过（`gofmt -l cmd internal tools`、`go test -count=1 ./...`、`go vet ./...`）；本条为历史记录，不代表当前工具链版本。

## 官方源码对照审计

- `设计待决定` 启动就绪判断存在提前接管代理的窗口：`Supervisor.Start` 仅等待 750ms 存活即发布 Running，托盘自动代理及配置重启随后即可写入系统代理；内核首次下载远程规则集时 router 初始化可能尚未结束，mixed 尚未监听。官方内核 `common/listener/listener.go` 的原生系统代理路径先监听再启用代理，官方 daemon 也在 `instance.Start()` 完成后发布 STARTED。本项为静态调用链确认，未实机复现；应单独处理代理启用时机，不靠增大固定延时或迁移 daemon 架构解决。
- `已完成` 官方源码已浅克隆到项目同级目录 `../sing-box-for-desktop-reference`（`d9bc9073fdd91d4343242bcff3790555cf9dff9f`）、`../sing-box-reference`（`927770c29f3e3698c711fc150e1066a4a78793a2`）和 `../sing-reference`（`6f21f2425a959912c37d2ef43d61e2a663315dea`），用于离线对照桌面入口、Windows 平台实现和底层 WinINet 逻辑；未安装依赖或构建官方客户端。
- `已完成` 静态对照发现的「关闭代理时可能恢复失效旧端口」已按官方行为修复并实机验证，实现与来源见「Windows 代理与 Discord 问题」。
- `设计待决定` `setProxySettings` 忽略三次 WinINet 变更通知的返回值，而官方所用 sing `6f21f2425a95` 的 `common/wininet/wininet_windows.go` 会逐次检查并返回错误；通知失败时本项目仍报告成功，读取设置不能证明其他应用已刷新代理。属于确定的错误丢失路径，实际通知失败尚未实机复现。

## 当前维护边界

后续每个功能点单独讨论、修改和验证；自动测试、静态检查和正式构建通过，只能证明代码路径和产物基本成立，不能替代 Windows/macOS 托盘、提权、睡眠唤醒、系统代理和真实网络的针对性验证。

## 目录结构

- `cmd/sing-box-drover`：程序入口；
- `internal/app`：应用编排；
- `internal/config`：INI、JSON 读取和 TUN 过滤；
- `internal/clash`：Clash API 访问、选择器读取和切换；
- `internal/core`：所管理的 `sing-box`/`sing-box.exe` 生命周期；
- `internal/tray`：Win32/AppKit 托盘、状态图标和选择器菜单渲染；
- `internal/windows`：跨平台系统集成层（Windows Win32、macOS networksetup/LaunchAgent/AppKit 通知），目录名保留以兼容现有导入；
- `resources`：Windows 应用图标/权限清单和 macOS `Info.plist`；
- `scripts`：可复现的本地构建脚本；
- `docs`：用户说明和开发资料；
- `tools`：国旗资源生成器等离线开发工具；
- `examples`：纳入版本控制的示例配置；
- `output`：不纳入版本控制的构建与本地运行目录。

## 本地构建

托盘控制器不内置 sing-box 内核。Windows 构建：

```powershell
$env:GOTOOLCHAIN = "go1.25.14"
go test ./...
go vet ./...
.\scripts\build.ps1
```

Windows 构建脚本使用固定版本的 Go 资源生成器，把 `resources/app.ico` 和
`resources/app.manifest` 临时生成到 `cmd/sing-box-drover/resource_windows_amd64.syso`，
再链接进 Windows GUI 程序；构建结束后会删除这个中间文件。GitHub Actions
调用同一脚本，因此发布产物也会带有应用图标和 `asInvoker` 清单。

macOS 构建需要安装 Xcode Command Line Tools，并启用 CGO：

```bash
GOTOOLCHAIN=go1.25.14 go test ./...
GOTOOLCHAIN=go1.25.14 go vet ./...
VERSION=dev ./scripts/build-macos.sh
```

脚本默认构建 arm64 + x86_64 Universal `.app`，保留包旁边的
`sing-box-drover.ini`，并执行 ad-hoc 签名。只构建当前架构时可设置
`GOARCH=arm64` 或 `GOARCH=amd64`；需要使用其他最低系统版本时可设置
`MACOS_MIN_VERSION`，同时保持 `Info.plist` 与 Mach-O 部署目标一致。

### macOS 本地运行

以下命令使用已忽略提交的 `output/local-dev` 进行单架构本地验证。把内核和配置
替换为实际路径，并先保持系统代理与 TUN 关闭：

```bash
RUN_DIR="$PWD/output/local-dev"
mkdir -p "$RUN_DIR"

GOARCH=arm64 VERSION=dev \
  ./scripts/build-macos.sh "$RUN_DIR/sing-box-drover.app"

cp /path/to/sing-box "$RUN_DIR/sing-box"
cp /path/to/config.json "$RUN_DIR/config.json"

cat > "$RUN_DIR/sing-box-drover.ini" <<EOF
[sing-box-drover]
sb-dir = $RUN_DIR
sb-config-file = config.json
system-proxy-auto = off
tun-start-mode = off
log-file = $RUN_DIR/sing-box-drover.log
homepage-url = https://github.com/moxuun/sing-box-drover-go
EOF

"$RUN_DIR/sing-box" --disable-color check -c "$RUN_DIR/config.json"
open "$RUN_DIR/sing-box-drover.app"
tail -f "$RUN_DIR/sing-box-drover.log"
```

另一终端可检查进程和监听端口：

```bash
ps -axo pid,ppid,stat,command | grep -E 'sing-box-drover|sing-box --disable-color'
lsof -nP -iTCP -sTCP:LISTEN | grep sing-box
```

来自浏览器下载的内核副本可能带有 `com.apple.quarantine`，会被 Gatekeeper 直接
终止。此时只对 `output/local-dev/sing-box` 副本执行 `xattr -cr`，不要修改原始
内核文件。普通权限验证通过后，再单独测试管理员提权、系统代理、TUN、
LaunchAgent 和睡眠唤醒。

`go.mod` 暂时将 Go 1.25.14 固定为低内存构建基线。`toolchain` 指令不会让
已经运行的 Go 1.27 自动降级，因此需要在 PowerShell 中设置
`$env:GOTOOLCHAIN = "go1.25.14"`。

## Go 版本和内存

本地复现表明，Go 1.26 和 Go 1.27 在 `net/http` 等 TLS 路径中会引入
32 MiB 的 FIPS 140 熵源缓冲区（`crypto/internal/fips140/drbg.memory`）。
即使未启用 FIPS，该缓冲区在 Windows 上也会成为驻留私有内存，使托盘进程
额外占用约 34 MB。上游问题 [`golang/go#78321`](https://github.com/golang/go/issues/78321)
记录了 Wasm 场景；Windows 工作集结论来自本地复现。在确认新版工具链修复前，
不要把 Go 1.27 当成解决方案。

## 自动发布

推送以 `v` 开头的版本标签（例如 `v0.1.0`）后，`.github/workflows/release.yml`
会在 Windows 和 macOS runner 上分别运行格式检查、测试、静态检查和构建，再由
单独的发布任务统一创建 GitHub Release。

Windows 发布包为 `sing-box-dover-go-v版本-windows-amd64.zip`，包含
`sing-box-dover-go.exe` 和兼容旧安装的 `sing-box-drover.ini`。

macOS 发布包为 `sing-box-drover-v版本-macos-universal.zip`，包含
`sing-box-drover.app` 和旁边的 `sing-box-drover.ini`，其中的 Universal
二进制同时支持 arm64 与 x86_64。两个平台都不包含 sing-box 内核；Release
同时提供 ZIP 的 SHA256 校验文件。

发布示例：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

打标签前先 `git fetch` 并确认 `main` 已经跟上 `origin/main`：标签一旦推上去
就会直接触发构建和发布。如果本地 `main` 落后于远端（例如远端刚合了 PR），
标签会落在一个不在 `main` 上的提交，只能删掉标签、取消对应的 workflow run、
rebase 之后重新打。

## 提交前检查

```powershell
gofmt -l cmd internal tools
go test -count=1 ./...
go vet ./...
```
