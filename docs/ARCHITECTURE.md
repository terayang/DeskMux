# DeskMux 架构与技术决策

> 状态：v2 待用户确认（2026-07-26）。本文档记录技术选型及理由，确认后作为开发依据。

## 0. 2026-07 Wails 迁移

2026-07-28 起项目已从 Electron 迁移到 **Wails v2**（Go 后端 + 系统 WebView）：Go 单进程承载原 Electron 主进程的全部协议层（`internal/scanner` / `sshx` / `rfb` / `vncbridge` / `store`），React 前端经 `frontend/src/bridge/` 的 `window.deskmux` 适配层零改动复用；密钥从 safeStorage 落盘改为系统钥匙串（go-keyring）。实测 mac 安装包 133 MB → 11 MB、首窗启动中位 340 ms → 241 ms。迁移报告（含架构对比、实测数据、已知限制）见 [MIGRATION.md](MIGRATION.md)。下文 §1–§9 为 Electron 时代的原始选型记录，保留作历史参考；凡与现状冲突处（运行时、包结构、测试与打包方式）以 MIGRATION.md 与现状为准。

## 1. 总体形态：Electron 桌面客户端

决策：**Electron**（主进程 Node.js + 渲染进程 Chromium），纯客户端架构 —— 应用运行在操作者的本地机器（Windows / Mac），直接向目标 IP 发起 TCP 连接，**目标机器零安装**。

候选对比：

- **Web 网关**（服务端跑在被控 Mac + 浏览器访问）：跨设备零安装，但不符合"原生桌面 APP 级体验"的要求，且需处理服务端暴露面 → 否决。
- **Tauri**：需 Rust 工具链（本机没有），且 VNC / 终端仍需 Web 技术渲染，收益有限 → 否决。
- **原生 Swift/AppKit**：无法复用 noVNC / xterm.js 生态，协议栈需全部自研，工作量数倍 → 否决。
- **Electron**：Node 主进程可直接实现 TCP/SSH/VNC 协议，渲染层复用 noVNC + xterm.js 成熟生态；[electerm](https://github.com/electerm/electerm)（Electron + ssh2 + xterm 的 SSH/SFTP 桌面客户端）已验证此路线 → **采用**。

跨平台约束（目标平台 macOS + Windows）：

- 只使用跨平台 API；路径一律经 `path` 模块；不硬编码路径分隔符。
- safeStorage 双平台透明（macOS Keychain / Windows DPAPI）。
- Apple DH 认证是协议层逻辑，与运行平台无关。
- 核心路径不使用任何平台专属 API；确需平台分支时在验收报告中说明。
- Windows 包不在开发机交叉构建，由 CI windows runner 产出。

## 2. 技术栈清单

| 层 | 选型 | 理由 |
|----|------|------|
| 语言 | TypeScript (strict) | 主进程 / 渲染进程统一 |
| 构建 | electron-vite | 2026 年 Electron 应用事实标准起点，Vite HMR，内置 react-ts 模板 |
| UI | React 18 + antd v5 | 生态与贡献者熟悉度最高；组件成熟（electerm 同款），避免自绘组件 |
| 状态管理 | zustand | 轻量（~1KB），会话 / 标签页等跨组件状态 |
| 国际化 | i18next + react-i18next | 最成熟的 JS i18n 方案；默认 zh-CN，预留 en-US |
| SSH/SFTP | ssh2 | 纯 JS，客户端 / 服务端双模式（测试可自建进程内 mock server） |
| 终端 | @xterm/xterm + fit / web-links 插件 | VSCode 同款终端内核 |
| 远程桌面 | noVNC（渲染层 RFB 客户端） | 成熟稳定，内置本地缩放（解决 TigerVNC 缩放问题） |
| 凭据存储 | Electron safeStorage → `userData/connections.json` | 系统钥匙串 / DPAPI 加密，免主密码 |
| 打包 | electron-builder → dmg (macOS) + NSIS (Windows) | 事实标准 |
| 测试 | vitest + Playwright Electron | 单测 / 集成 + E2E 冒烟 |
| CI | GitHub Actions（macos + windows 矩阵） | lint + test + build |

## 3. 自研与开源的边界（2026-07-26 审计）

原则：能用成熟开源（star 高、维护活跃）就不自研。star 数为该时点近似值。

| 能力 | 方案 | 结论 |
|------|------|------|
| 桌面壳 / 构建 / UI / 状态 / i18n / SSH / 终端 / VNC 渲染 / 加密 / 打包 / 测试 | 见 §2（Electron ~117k★、antd ~95k★、zustand ~50k★、i18next ~8k★、ssh2 ~5.9k★、xterm.js ~18k★、noVNC ~12k★、electron-builder ~14k★、vitest ~14k★、Playwright ~70k★） | 全部采用成熟开源 |
| 协议指纹扫描 | node-libnmap 需预装 nmap 二进制，不可嵌入；grab.js 为玩具级 CLI | **自研（约 200 行）**：`net` 并发连接 + banner 抓取 + RDP 协商探测，无成熟可嵌入库 |
| WS↔TCP 桥接 | websockify 是 Python 程序，需解释器，不可嵌入 Electron | **自研（约 50 行）**：`ws` + `net` 管道 |
| Apple DH 认证（RFB type 30） | noVNC 明确不支持（[issue #1522](https://github.com/novnc/noVNC/issues/1522)）；libvncclient（C）需原生编译，双平台打包成本高 | **自研（约 150 行）**：Node 内置 `crypto`（DH + AES-128 + MD5），参照 TigerVNC `CSecurityDH` 移植，对本机 5900 真实服务器做协议级验证 |

三处自研均为薄 glue 层，总计约 400 行，均有明确测试方案（单测 + mock + 真实服务器协议级验证）。

## 4. 模块结构

```
deskmux/
├─ package.json / tsconfig.json / electron.vite.config.ts
├─ src/
│  ├─ main/                  # Electron 主进程
│  │  ├─ index.ts            # 应用入口、窗口管理
│  │  ├─ scanner.ts          # 协议探测（端口扫描 + 指纹识别）【自研 glue】
│  │  ├─ sshService.ts       # SSH/SFTP 会话管理（ssh2）
│  │  ├─ vncBridge.ts        # WS↔TCP 桥接（每会话一个本地 WS 端点）【自研 glue】
│  │  ├─ rfb/                # RFB 握手、security type 2/30 认证【自研 glue】
│  │  ├─ store.ts            # 连接配置持久化 + safeStorage 凭据加密
│  │  └─ ipc.ts              # IPC 通道定义与注册
│  ├─ preload/index.ts       # contextBridge 安全暴露 API
│  ├─ renderer/              # React UI
│  │  ├─ App.tsx
│  │  ├─ pages/              # ScanPage（扫描+协议卡片）、SessionPage（标签页）
│  │  ├─ components/         # Terminal、FileManager、VncView、ProtocolCard
│  │  ├─ store/              # zustand stores
│  │  └─ i18n/               # i18next 配置、zh-CN.json、en-US.json
│  └─ shared/                # 主/渲染共享类型与协议常量
├─ tests/                    # vitest 单测/集成 + Playwright E2E
├─ docs/                     # 文档
├─ _ref/                     # 第三方参考源码（不入库）
└─ .github/workflows/        # CI（macos + windows 矩阵）
```

## 5. 关键决策：macOS Screen Sharing 认证

实测本机 5900：RFB 003.889，仅提供 Apple 专有 security type（30/33/35/36），标准 VNC 客户端（含 noVNC）无法直连。

[TigerVNC 1.14.0（2024）](https://github.com/tigervnc/tigervnc/releases) 已在原生 viewer 中实现 Apple Diffie-Hellman 认证（type 30）—— 证明客户端侧实现完全可行，且有开源 C++ 实现（`CSecurityDH`）可参照移植。

决策：**主进程 VNC 桥接层终结 RFB 安全握手** —— 桥与服务器完成 type 30（Apple DH）/ type 2（标准 VNC 密码）认证后，向 noVNC 侧呈现 "None" 安全类型，noVNC 无需任何修改。

兜底：若 Apple DH 与 macOS 26 存在兼容问题，输出一条 `kickstart` 命令（开启标准 VNC 密码）请用户执行一次，**不自行 sudo**。

## 6. 协议探测设计

主进程对目标并发发起 TCP 连接（端口表：22/23/21/3389/5900/445/80/443/8080/6200…），按指纹判定：

- **SSH**：读取 `SSH-2.0-*` banner（服务器主动发送）
- **VNC**：读取 `RFB xxx.xxx` banner（服务器主动发送）
- **RDP**：发送 X.224 连接请求，解析协商响应
- **HTTP(S)**：发送 HEAD 请求 / TLS 探测
- **Telnet / FTP / SMB**：banner / 特征字节

单端口超时 2s，结果聚合成协议卡片列表（名称、端口、通俗说明、支持状态）。

## 7. 测试策略

1. **单测**（vitest）：指纹解析器、RFB 消息编解码、Apple DH crypto（对已知互操作测试向量）。
2. **集成测试**：ssh2 server 模式起进程内 mock SSH/SFTP fixture，走真实 IPC 链路做端到端；mock RFB server 测桥接。
3. **协议级真实验证**：对本机 5900 完成握手至凭据校验前所有环节（无需密码）。
4. **E2E**（Playwright Electron）：启动应用冒烟 —— 扫描 fixture、建立会话、终端回显。
5. **CI 矩阵**：macos runner 跑全部测试 + 构建 dmg；windows runner 跑全部测试 + 构建 NSIS 安装包。
6. **UAT**（需用户一次）：Windows 安装后输入 macOS 密码完成真实 VNC 登录，验证授权弹窗可点击。

## 8. 产品体验设计原则

目标：对标 1Remote / Tabby / Termius 的专业远程工具质感，美观且高效。

- **视觉**：深色主题为默认（antd `darkAlgorithm` + compact 紧凑密度 + 定制 token：主色 / 圆角 / 字体）；终端与地址类文本使用等宽字体（JetBrains Mono / SF Mono）。
- **信息架构**：工作台即首页 + Operator Console 骨架（2026-09 起）——48px 活动栏（会话/设备视图切换 + ⌘K 快速连接 + 设置）| 240px 上下文理表（会话视图：活跃会话组 + 已保存设备组，实时筛选；设备视图：设备清单）| 主内容区（无会话=欢迎面板快速连接；有会话=44px 会话头 + 自研 32px 协议标签条 + 面板；设备视图=设备表，行内 [终端]/[桌面] 直连入口）。⌘K 命令面板为全局叠加层：搜索设备/会话、切换会话、扫描新地址、新建连接。新目标统一经「新建连接」模态完成（输入目标 → 协议卡片多选 → 凭据）。旧的"扫描向导首页"与 Sider+Tabs 布局已退役；设备/会话的身份匹配规则：savedId 优先，无 savedId（连接后才保存）时回退 host+username（utils/session.ts 的 savedForSession/liveSessionFor）。
- **多会话并存（F6）**：连接新目标 = 新增会话而非替换；每个会话有前端生成的 session id（`session-N`），其协议标签页子树常驻 DOM（非活跃会话 display:none 隐藏，终端缓冲/noVNC 画布/连接均保留）；面板经 `SessionScopeContext`（store/session.ts）读取本会话上下文，terminal/files/vnc 的 zustand store 为按 session id 注册的实例（getXxxStore/dropXxxStore），VNC live 槽为 `Map<sessionId, …>`；关闭会话释放其全部资源，全部关闭后回落欢迎面板。
- **交互**：键盘优先（`Cmd/Ctrl+K` 命令面板、`Cmd/Ctrl+1..9` 切换第 N 个会话、`Cmd/Ctrl+W` 关闭标签；全局快捷键走 capture 阶段监听——xterm 会吞掉 Ctrl+K 等按键的冒泡）；协议卡片 = 图标 + 名称 + 一句话人话说明 + 支持状态；连接进度与错误反馈即时可见，错误信息用中文人话。
- **评审流程**：阶段 1 交付 mock 数据的可点击 UI 原型（Playwright 截图），用户确认视觉与交互方向后才进入功能实现；后续每个 UI 相关阶段附最新截图。

## 9. 阶段计划

| 阶段 | 内容 | 完成标志 |
|------|------|----------|
| 0 | 脚手架（electron-vite + react-ts 模板、CI 矩阵、README/LICENSE） | 应用窗口可启动，CI 绿 |
| 1 | UI 原型（mock 数据：扫描页 + 协议卡片 + 会话工作区 + 文件管理器框架）+ 设计评审 | 用户确认视觉与交互方向 |
| 2 | 扫描器 + 协议卡片接入真实数据 | 扫描本机返回 SSH+VNC 卡片，单测绿 |
| 3 | SSH 终端会话 | 对 mock server 终端回显正常 |
| 4 | SFTP 文件管理 | 对 mock server 完成增删传下载 |
| 5 | VNC 桥接 + Apple DH + noVNC 接入 | 对本机 5900 协议级握手通过 |
| 6 | 连接保存（safeStorage）+ 多标签 + i18n 打磨 | 凭据加密落盘，界面中英文切换 |
| 7 | 测试补齐 + dmg/NSIS 打包 + 验收报告 | 验收标准全过 |
