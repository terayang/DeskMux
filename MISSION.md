# DeskMux 任务使命书（MISSION）

> 本文档是经用户确认的任务契约，是长周期自主开发的唯一权威依据。
> 任何方向性变更（产品形态 / 功能范围 / 技术栈）必须与用户重新确认后方可修改本文档。
> 状态：v2 待用户最终确认（2026-07-26）
>
> **v3 附注（2026-07-28）**：技术栈已迁移至 Wails v2（Go 后端 + 系统 WebView，见 [docs/MIGRATION.md](docs/MIGRATION.md)）；F5 凭据存储改为可配置（系统钥匙串 / 本机加密文件）。本文 §5 技术栈一节为历史记录，现状以迁移文档与 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) §0 为准。

## 1. 背景与痛点

用户通过 VSCode Remote-SSH 远程使用 macOS 开发机（silicamacbook-pro，Tailscale IP `100.115.254.53`，macOS 26.3），无法看到屏幕；当 macOS 弹出授权对话框（如录屏 / 辅助功能授权）时无法点击，工作流中断。

参考项目 [1Remote](https://github.com/1Remote/1Remote)（Windows 平台远程会话管理器）：支持 RDP/SSH/VNC/SFTP 等多协议，但完全依赖手动配置，无协议自动探测，且仅支持 Windows。

## 2. 目标

开发跨平台桌面应用 **DeskMux**：输入目标 IP → 自动探测其可用的远程协议 → 用户多选（每个协议附通俗说明）→ 一键建立连接，获得远程桌面、终端、文件管理能力。体验对标原生桌面应用。

**目标平台：macOS 与 Windows**（AI 在 macOS 上开发与自验；用户在 Windows 上验收使用）。

## 3. 功能需求（MVP 范围）

| # | 功能 | 说明 |
|---|------|------|
| F1 | 协议自动探测 | 对目标 IP 并发探测常见端口 + 协议指纹识别（SSH/VNC/RDP/Telnet/FTP/HTTP(S)/SMB 等），以卡片形式展示，每张卡片附一句通俗说明，支持多选 |
| F2 | VNC 远程桌面 | noVNC 渲染 + 主进程智能桥接；桥内实现 Apple 专有 DH 认证（RFB security type 30，参考 TigerVNC 1.14+ 实现）与标准 VNC 密码认证（type 2），连接 macOS Screen Sharing 无需 sudo 改系统配置；支持"适应窗口 / 原始尺寸"缩放 |
| F3 | SSH 终端 | xterm.js + ssh2，支持密码与私钥认证 |
| F4 | SFTP 文件管理 | 远程文件浏览、上传 / 下载、新建 / 删除 / 重命名 |
| F5 | 连接管理 | 保存主机配置；凭据经 Electron safeStorage（系统钥匙串 / DPAPI）加密存本地 |
| F6 | 多标签会话 | 同一 / 不同目标的多协议会话以标签页并存 |
| F7 | 国际化 | 界面默认简体中文，i18n 框架预留英文等多语言 |
| F8 | 产品体验 | 深色专业工具风（对标 1Remote / Tabby / Termius）；键盘优先；先交付 mock 数据的可点击 UI 原型，经用户评审确认视觉与交互方向后再全面实现功能 |

## 4. 非目标（MVP 明确不做）

- RDP / Telnet / FTP 的实际连接（仅探测展示，并提示"暂不支持，规划中"）
- 应用内账号体系 / 额外权限管理（安全完全交由目标协议自身的认证机制）
- 被控端 agent（DeskMux 是纯客户端，不在目标机器安装任何组件）
- Linux 安装包（架构保持跨平台；MVP 产出 macOS 与 Windows 安装包）

## 5. 技术栈

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。要点：

- Electron + electron-vite（主进程 Node.js：网络与协议层；渲染进程：UI）
- TypeScript 严格模式；React 18 + antd v5；zustand（状态）；i18next + react-i18next（默认简体中文）
- ssh2（SSH/SFTP）、@xterm/xterm（终端）、noVNC（远程桌面渲染）
- 仅三处薄 glue 层自研：协议指纹扫描器、WS↔TCP 桥接、Apple DH 认证握手 —— 已经调研确认无成熟可嵌入方案，证据见架构文档 §3
- Electron safeStorage（凭据加密）、electron-builder（macOS dmg + Windows NSIS）
- vitest + Playwright Electron（测试）、GitHub Actions（CI，macOS + Windows 双平台矩阵）

## 6. 验收标准

1. `npm test` 全绿：指纹解析、RFB 编解码、Apple DH 认证（对已知测试向量）单测；基于 ssh2 内建 mock server 的 SSH/SFTP 端到端集成测试；mock RFB server 的桥接测试。
2. 对本机 5900 完成协议级真实握手验证（认证流程走通到凭据校验前的所有环节）；完整真实登录为 UAT 步骤，需用户输入一次 macOS 密码。
3. CI（macOS + Windows 双 runner）全绿并产出：macOS `.dmg` 与 Windows 安装包（NSIS）。AI 在本机完成 macOS 侧功能验证；用户在 Windows 完成安装 UAT：扫描 `192.168.50.43`（本机内网 IP；Windows 经内网直连，MacBook Air 侧走 Tailscale `100.115.254.53`）→ 勾选 SSH+VNC → 终端可执行命令、文件可浏览传输、桌面可见可操作（可点击 macOS 授权弹窗）。
4. 开源标准齐备：README（中英）、LICENSE（MIT）、CI 绿灯、目录结构清晰。

## 7. 工作模式

- 用户把控方向；本文件确认后，AI 进入长周期自主开发与自测，验收标准全部通过后提交验收报告。
- 开发模式：**主 Agent 编排 + coder 子代理实现** —— 架构决策、接口契约、关键技术验证（如 Apple DH 握手）、代码审查与验收测试由主 Agent 亲自负责；边界清晰的实现任务委派子代理开发，交付后主 Agent 审查 diff 并运行测试，不合格打回重做。每个阶段完成运行完整测试套件并留存证据。
- 实现细节（库版本、目录命名、内部 API 设计等）AI 自主决策；方向性变更必须重新确认。
- 开发期间在本机真实环境验证；不修改项目目录外的系统配置（确需 sudo 的操作一律改为输出命令请用户执行）。
