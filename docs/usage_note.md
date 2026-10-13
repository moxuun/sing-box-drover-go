# 使用说明

本程序通过 mygo 在 Windows、macOS 和 Linux 托盘中管理 sing-box，包括系统代理、TUN、节点选择和内核重启。

## 准备文件

下载解压后，请把以下文件放在同一个文件夹下：

| 文件                    | 说明                                                           |
| ----------------------- | -------------------------------------------------------------- |
| `sing-box-dover-go.exe` | 控制器本体；macOS 为 `sing-box-drover.app`，Linux 为 `sing-box-drover` |
| `sing-box.exe`          | sing-box 内核；macOS/Linux 为 `sing-box`，需自行准备 |
| `config.json`           | 你的 sing-box 配置文件（必须是已能正常工作的配置）             |
| `sing-box-drover.ini`   | 控制器设置文件（压缩包内已自带默认配置）                       |

---

> **提示**：建议将程序存放在有普通读写权限的目录（如自建文件夹），尽量避免放在 `C:\Program Files` 等受系统写保护的目录下，以免无法正常生成日志。

## 使用方法

运行控制器后不会弹出常规窗口，程序常驻托盘。Windows 位于任务栏通知区，macOS 位于顶部菜单栏，Linux 由桌面的 StatusNotifier/AppIndicator 宿主显示。

### 快捷操作

- **Windows 单击托盘图标**：快速开启或关闭系统代理。
- **Windows Shift + 单击**：快速开关 TUN；需要权限时显示 UAC。
- **macOS / Linux**：展开托盘菜单，点击 `System Proxy` 或 `TUN`。

### 图标状态

- ⚪ **灰色 / 无色**：代理未开启（直连状态）
- 🟢 **绿色**：系统代理或 TUN 正在运行中
- 🔴 **红色**：内核启动失败或异常退出（鼠标悬停可查看错误提示）

### 托盘右键菜单

| 菜单项                         | 说明                                                                              |
| ------------------------------ | --------------------------------------------------------------------------------- |
| **节点列表**                   | 直接点击即可切换出站节点。若指向自动选择组（URLTest），会标出当前实际落点的节点。 |
| **System Proxy**               | 勾选表示已开启系统代理。                                                          |
| **TUN**                        | 开关虚拟网卡模式（仅在配置中有 `tun` 入站时显示）。                               |
| **Start with Windows / macOS / Linux** | 随当前用户登录启动；Linux 写入 XDG 自启动项。 |
| **Restart core**               | 重启内核进程。修改了 `config.json` 后点此生效。                                   |
| **Homepage**                   | 在浏览器中打开指定页面（可在 ini 中设为 Web 控制台网址）。                        |
| **Quit**                       | 退出控制器并同步停止内核。                                                        |

## Linux

运行需要 GTK 3 和 `libayatana-appindicator3` 或 `libappindicator3`。桌面还要提供 StatusNotifier/AppIndicator 托盘宿主；GNOME 通常需要安装并启用 AppIndicator 扩展。

系统代理目前支持 GNOME 的 GSettings，设置 HTTP、HTTPS 和 SOCKS 代理，并在正常关闭时恢复原设置。若用户或其他软件在运行期间修改了代理，控制器保留外部修改。非 GNOME 桌面会提示不支持，可使用 TUN 或在应用内配置代理。

开启 TUN 前，sing-box 需要具有 `CAP_NET_ADMIN`。将下面路径替换为实际内核文件；这条命令由用户手动执行：

```bash
sudo setcap cap_net_admin,cap_net_raw+ep /absolute/path/to/sing-box
getcap /absolute/path/to/sing-box
```

更新或替换内核后需要重新设置权限。控制器会在启动或替换内核前检查 TUN 权限；检查失败时保留当前运行的内核。托盘本身以普通用户运行。

开启 `Start with Linux` 会写入 `${XDG_CONFIG_HOME:-~/.config}/autostart/sing-box-drover.desktop`。控制器移动路径后，请重新开启自启动。

## 配置要求 (config.json)

程序不会修改你的 `config.json` 原文件。为了让托盘功能正常运作，配置中需要包含以下对应配置项：

1. **系统代理**：需要包含 `mixed` 或 `http` 类型的入站规则，并指定监听端口：
   ```json
   {
     "type": "mixed",
     "tag": "mixed-in",
     "listen": "127.0.0.1",
     "listen_port": 2080
   }
   ```
2. **节点选择**：需要开启 Clash API，并包含至少一个 `selector` 类型的出站组：
   ```json
   "experimental": {
     "clash_api": {
       "external_controller": "127.0.0.1:9090"
     }
   }
   ```
3. **TUN 模式**：需要配置中包含 `tun` 类型的入站规则（没有的话菜单中不会出现 TUN 选项）。
4. **注意**：如果配置中写了 `"set_system_proxy": true`，请改为 `false`，否则内核会与托盘争夺代理控制权。

## 控制器设置 (sing-box-drover.ini)

通常保持默认即可直接使用。如需个性化调整，可以用记事本编辑同目录下的 `sing-box-drover.ini`：

| 配置项                 | 默认值        | 说明                                                                              |
| ---------------------- | ------------- | --------------------------------------------------------------------------------- |
| `system-proxy-auto`    | `off`         | 是否在启动控制器时自动打开系统代理（`on` / `off`）                                |
| `tun-start-mode`       | `off`         | 是否在启动控制器时自动打开 TUN 模式（`on` / `off`）                               |
| `selector-menu-layout` | `auto`        | 节点菜单展示方式：`auto`、`flat`（平铺展示）或 `nested`（多节点时使用子菜单折叠） |
| `homepage-url`         | 空            | 菜单中「Homepage」打开的网址（如面板地址 `http://127.0.0.1:9090/ui/`）            |
| `log-file`             | 空            | 日志保存路径。填文件名如 `sing-box-drover.log` 记录日志，留空则不写文件（示例配置中默认开启） |
| `sb-dir`               | 空            | 内核所在目录。留空默认在当前目录寻找 `sing-box.exe` / `sing-box`                  |
| `sb-config-file`       | `config.json` | 配置文件名称或相对路径                                                            |

修改 ini 保存后，需要重启控制器生效。

## 常见排查

- **双击后没有图标，也没有反应？**  
  通常是内核启动失败或配置存在语法错误。可以在终端/命令提示符中执行 `sing-box check -c config.json` 检查具体报错。
- **图标变成红色？**  
  鼠标悬停在托盘图标上即可查看错误提示。常见原因是端口被其他代理程序占用，或配置文件格式错误。
- **菜单里看不到节点？**  
  检查 `config.json` 中是否配置了 `selector` 类型的出站，并且是否正确启用了 `experimental.clash_api`。
- **macOS 提示“应用已损坏”或无法打开？**  
  这是 macOS Gatekeeper 安全拦截。在终端运行以下命令解除隔离属性即可：
  ```bash
  xattr -dr com.apple.quarantine /path/to/sing-box-drover.app
  # 若内核也是从浏览器直接下载的，同样对其执行：
  xattr -cr /path/to/sing-box
  ```
- **Linux 没有托盘图标？**
  确认 GTK 3、AppIndicator 库和桌面托盘宿主均可用，并从桌面终端启动控制器查看错误。
- **Linux 系统代理提示不支持？**
  当前实现只接入 GNOME GSettings。其他桌面可使用 TUN，或在应用中填入 sing-box 的代理地址。
- **Linux TUN 提示权限不足？**
  检查 sing-box 的 `CAP_NET_ADMIN`；替换内核后重新设置，参见上面的 Linux 说明。
- **如何彻底清理卸载？**  
  右键托盘菜单选择退出。若开启过开机自启，请先在菜单中取消勾选，之后直接删除整个程序文件夹即可。
