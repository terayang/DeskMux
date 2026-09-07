# Changelog

本文件记录 DeskMux 各版本的显著变更，遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 简式与[语义化版本](https://semver.org/lang/zh-CN/)。

各版本完整的自动生成 release notes 见 [GitHub Releases](https://github.com/terayang/DeskMux/releases)。

## [0.2.2] - 2026-09-07

### Changed

- 产品更名 AnyRemote → DeskMux：仓库、安装产物、窗口标题、界面文案、Go module 与前端桥接命名空间（`window.deskmux`）全部同步；配置目录、钥匙串 service 名与本地加密文件 KDF 盐保留旧标识以保用户数据连续性（已存连接与凭据无感）
- 界面语言偏好存储键随桥接改名更换，语言选择一次性回落默认中文

## [0.2.1] - 2026-09-07

### Added

- 多会话并存（MISSION F6）：同时连接多个目标，Sider 活跃会话区切换/独立关闭，终端滚动缓冲与 VNC 画面随切换保留
- 工作台即首页：启动直进工作台，无会话时欢迎面板提供已保存设备列表与快速连接入口；协议自动探测收编为「新建连接」模态流程
- Operator Console 工作台骨架：48px 活动栏（会话/设备视图 + ⌘K + 设置）+ 240px 上下文理表（活跃会话/已保存设备分组、实时筛选）+ 主内容区（会话头 + 自研协议标签条）；替代原 Sider+Tabs 布局
- ⌘K 命令面板：搜索设备与会话、切换会话（⌘1..⌘9 直达第 N 个）、扫描新地址、新建连接
- 打包命令自动递增 patch 版本号（`scripts/bump-version.mjs` 作为 dist/dist:win 的 pre 钩子，CI 环境自动跳过），本地构建产物文件名唯一、互不覆盖
- 设备视图：设备清单一等公民，行内 [终端]/[桌面] 按协议直连（复用已存凭据）、编辑/删除、「接入新设备」入口
- 已保存连接支持编辑（名称/host/用户名/协议/凭据；协议可勾选 SSH/VNC，扫描残留的只读协议原样保留；凭据留空保留、可显式清除）；打开编辑时后台探测主机协议，已开放但未保存的协议就地提示一键添加
- VNC 桌面：双向剪贴板同步（RFB CutText；仅 Latin-1，中文受协议限制）、全屏切换、只读模式、发送特殊按键（Ctrl+Alt+Del / Alt+F4 / Super）
- SSH 会话 keepalive（TCP + SSH 应用层双层保活，连续失败自动判定断线）

### Changed

- 已保存连接卡片改为单击直连（原双击；单击回填地址会因快捷横幅顶位移布局导致双击落空）
- 语言切换从应用页头移入设置模态
- 全局快捷键改 capture 阶段监听（xterm 会吞掉 Ctrl+K 等按键的冒泡，终端聚焦时 ⌘K 曾失效）

### Fixed

- 首页硬编码开发机地址（192.168.50.43）导致启动后只显示匹配设备、其余已保存设备被输入框过滤器隐藏；返回首页时残留地址同样过滤列表
- VNC 剪贴板第一版实现依赖 paste 事件，但浏览器只在可编辑元素上派发且 noVNC 会 preventDefault 画布按键——真实键盘下永不触发；改为捕获阶段拦截 + 隐藏 textarea 承接粘贴命令
- 扫描路径连接后才保存的设备不被识别为同一会话（会话显示原始 IP、设备行无「已连接」徽标）；身份匹配回退 host+username

## [0.1.2] - 2026-07-28

### Changed

- 安装包产物统一为带版本号与平台的命名：`AnyRemote-<version>-mac-arm64.dmg` / `-mac-x64.dmg` / `-windows-x64-installer.exe` / `-windows-x64-portable.exe`

## [0.1.1] - 2026-07-28

### Added

- Windows NSIS 安装包与免安装便携版，由 CI macos runner 交叉构建（wails 内置 makensis）

## [0.1.0] - 2026-07-28

### Added

- 首个基于 Wails v2 的公开版本（自 Electron 迁移）：协议自动探测、VNC 远程桌面（含 macOS Screen Sharing Apple DH 认证）、SSH 终端、SFTP 文件管理、多标签会话、连接管理（系统钥匙串 / 本机加密凭据）、中英双语界面
- 打版本 tag 自动构建并发布 GitHub Release 的流水线（macOS + Windows）

[0.1.2]: https://github.com/terayang/AnyRemote/releases/tag/v0.1.2
[0.1.1]: https://github.com/terayang/AnyRemote/releases/tag/v0.1.1
[0.1.0]: https://github.com/terayang/AnyRemote/releases/tag/v0.1.0
