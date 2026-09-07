# 安全政策 / Security Policy

[English](#security-policy-1) | [中文](#安全政策)

---

## 安全政策

### 支持的版本

DeskMux 处于 0.x 阶段，仅最新发布版本获得安全修复：

| 版本 | 支持状态 |
|------|----------|
| 0.1.x（最新） | ✅ 支持 |
| < 0.1.0 | ❌ 不再支持 |

### 报告漏洞

**请勿通过公开 Issue 报告安全漏洞。**

请发送邮件至 **silicayang@gmail.com**，尽量包含：

- 受影响的版本与平台
- 复现步骤或概念验证（PoC）
- 影响评估（如可能）

响应承诺：

- **72 小时内**确认收到报告
- **7 天内**给出初步评估与处置计划
- 修复发布后，经报告者同意可在 Release Notes 中署名致谢

### 凭据与密钥处理原则

DeskMux 处理大量远程连接凭据，安全设计遵循以下原则：

- **系统钥匙串优先**：密码 / 私钥默认存 macOS Keychain / Windows Credential Manager（经 go-keyring）
- **本地加密备选**：可在设置中切换为本机加密文件（AES-256-GCM，密钥由机器硬件 UUID 派生）
- **绝不明文落盘**：`connections.json` 只存元数据，不含任何秘密
- **不联网上报**：无任何遥测 / 统计上报；凭据不会离开本机（除用户主动发起的远程连接本身）
- **不写入日志**：秘密不出现在任何日志与错误信息中

### 已知安全注意事项

- 当前安装包**未做代码签名与公证**，首次启动的系统警告属预期，安装指引见 [docs/RELEASE.md](docs/RELEASE.md)
- SSH 主机密钥校验、VNC 加密通道等协议层加固见 Roadmap，欢迎贡献

---

## Security Policy

### Supported versions

DeskMux is in 0.x; only the latest release receives security fixes:

| Version | Supported |
|---------|-----------|
| 0.1.x (latest) | ✅ |
| < 0.1.0 | ❌ |

### Reporting a vulnerability

**Please do NOT report security vulnerabilities through public issues.**

Email **silicayang@gmail.com** with the affected version and platform, reproduction steps or a PoC, and an impact assessment if possible.

- Acknowledgement within **72 hours**
- Initial assessment and remediation plan within **7 days**
- With your consent, credit in the release notes once the fix ships

### Secrets handling principles

- Passwords / private keys go to the OS keychain by default (macOS Keychain / Windows Credential Manager, via go-keyring)
- Optional local encrypted file (AES-256-GCM, key derived from the machine hardware UUID)
- `connections.json` stores metadata only — never secrets
- No telemetry: secrets never leave the machine (beyond the remote connections you initiate)
- Secrets are never written to logs or error messages

### Known security notes

- Installers are **not code-signed or notarized** yet; first-launch OS warnings are expected — see [docs/RELEASE.md](docs/RELEASE.md)
- Protocol-level hardening (SSH host-key verification, encrypted VNC transport) is on the roadmap — contributions welcome
