# 参与贡献 / Contributing

[English](#contributing-english-summary) | [中文](#参与贡献)

感谢你对 DeskMux 的兴趣！本文档说明开发环境、常用命令、代码规范与 PR 流程。

## 参与贡献

### 前置环境

- Go 1.26
- Node.js 22+
- wails CLI v2.13：`go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`（确保 `~/go/bin` 在 PATH）

### 安装依赖

```bash
npm install && npm --prefix frontend install
```

### 开发

```bash
npm run dev                        # wails dev（前后端热重载）
npm --prefix frontend run dev      # 纯前端预览（vite），bridge 自动切 mock，无需 Go 侧运行
```

### 测试与类型检查

提交前请确保本地全绿：

```bash
npm test           # Go 测试（go test ./...，93 个）
npm run typecheck  # go vet ./... + 前端 tsc --noEmit
```

### 构建与打包

```bash
npm run build      # wails build → build/bin/（同时重新生成 frontend/wailsjs/ 绑定）
npm run dist       # macOS arm64 + x64 dmg → dist/
npm run dist:win   # Windows NSIS 安装包 + 便携版 → build/bin/
```

### 代码规范

- **TypeScript**：strict 模式；代码注释用英文
- **Go**：`gofmt` 格式化、`go vet` 通过；代码注释用英文
- **界面文案**：默认简体中文，走 i18n 字典（`frontend/src/i18n/zh-CN.json` 与 `frontend/src/i18n/en-US.json`），新增 key 两边必须同时补齐
- **凭据安全**：密码 / 私钥永不入库、不写入日志；存取一律经 `internal/store`（系统钥匙串或本机 AES-256-GCM 加密文件）
- 最小改动原则；新代码风格与周边文件保持一致
- 优先采用维护活跃的成熟开源方案；引入新依赖前请先在 issue 中讨论

### 提交信息

使用英文，遵循 [Conventional Commits](https://www.conventionalcommits.org/) 简式：

- `feat: ...` 新功能
- `fix: ...` 缺陷修复
- `docs: ...` 文档
- `chore: ...` 杂项（依赖、工具等）
- `ci: ...` CI / 构建
- `test: ...` 测试

示例：`fix: restore full-height workspace — antd App wrapper broke the height chain`

### PR 流程

1. Fork 本仓库，从 `main` 切出特性分支（如 `feat/rdp-connect`）
2. 完成开发，确保 `npm test` 与 `npm run typecheck` 全绿
3. 提交 PR 并填写模板（变更说明、类型、测试证据、关联 issue；UI 变更附截图）
4. CI（macOS + Windows 双平台）全绿后等待 review；review 意见请在原分支追加提交回应

## Contributing (English summary)

Thanks for your interest in DeskMux!

- **Prerequisites**: Go 1.26, Node.js 22+, wails CLI v2.13 (`go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0`, with `~/go/bin` on PATH)
- **Setup**: `npm install && npm --prefix frontend install`
- **Develop**: `npm run dev` (wails dev with HMR); pure-frontend preview: `npm --prefix frontend run dev` (bridge falls back to mock)
- **Verify before submitting**: `npm test` (`go test ./...`, 93 tests) and `npm run typecheck` (`go vet ./...` + frontend `tsc --noEmit`) must both pass
- **Build**: `npm run build` (→ `build/bin/`, regenerates `frontend/wailsjs/` bindings); installers: `npm run dist` (macOS arm64 + x64 dmgs) / `npm run dist:win` (Windows NSIS + portable)
- **Style**: TypeScript strict; Go gofmt-formatted and vet-clean; comments in English; UI copy goes through the i18n dictionaries — add new keys to both `frontend/src/i18n/zh-CN.json` and `frontend/src/i18n/en-US.json`; never commit credentials or write them to logs
- **Commits**: English, Conventional Commits (`feat` / `fix` / `docs` / `chore` / `ci` / `test`)
- **PRs**: fork → feature branch → fill in the PR template → CI green (macOS + Windows) → review
