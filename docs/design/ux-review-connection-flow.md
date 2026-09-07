# AnyRemote 连接管理交互评审与修改方案

> **2026-09 更新（取代部分结论）**：应用首页已从"扫描向导页"改为"工作台页"——启动直进 SessionPage，无会话时主区为欢迎面板（快速连接输入 `#target-address-input` + 已保存设备列表，保留 B1/B5/B6 语义），扫描流程统一收口到 `NewConnectionModal`；`ScanPage` 与 store 的 `page` 字段已删除，⌘K 始终打开新建连接模态，断开/关完标签停留在工作台空态。下文 A4"按 page 分发"、B 系列"首页"等表述以本次更新为准。

> 评审对象：首页扫描 → 凭据 → 会话工作区的连接管理交互（v0.0.1，2026-07-27）
> 评审依据：src/renderer/pages/{ScanPage,SessionPage}.tsx、src/renderer/store/{index,session,savedConnections}.ts、src/renderer/components/CredentialsModal.tsx、MISSION.md、docs/ARCHITECTURE.md §8
> 结论先行：两个用户反馈均成立。根因是"扫描页被设计成发起连接的唯一场所"且"输入框与已保存列表零联动"。v1 用两个常驻入口 + 输入匹配提示条解决，不动单会话模型；v2 再演进到多目标并行。

## 1. 问题分析

### 1.1 现状流程（代码事实）

```
[扫描页 ScanPage]
  输入 IP ──开始扫描──▶ 协议卡片多选 ──连接──▶ CredentialsModal ──▶ beginSession()
     ▲                                                                    │
     │                                                                    ▼
[已保存列表]                                              [会话页 SessionPage]
  单击 = 把 host 填进输入框（→重新扫描）                     左 sider：当前会话 + 已保存列表
  双击/连接按钮 = 直连（connectSaved）                       单击已保存项 = 断开当前、切到新目标
                                                            右 主区：desktop/terminal/files 标签
     ▲                                                                    │
     └────────────── 关闭最后一个标签页（closeTab）◀───────────────────────┘
```

关键事实：

- `App.tsx` 只有两个页面互斥渲染；⌘K 聚焦 `#target-address-input`，但该元素只存在于扫描页——**会话页里 ⌘K 是死快捷键**。
- `store/index.ts` 定义了 `disconnect()`，但**没有任何 UI 调用它**。离开会话的唯一路径是逐个关掉所有标签页。
- 会话页 sider 的已保存项点击即切换目标，但只能切到**已保存过**的目标；新 IP 没有任何入口。
- 扫描页已保存条目：单击=填输入框（引导重新扫描），双击=直连——双击语义无任何可见提示，发现性为零。
- 单会话模型：`beginSession`/`beginSavedSession` 先 `resetPanelStores()`，再整体替换 `SessionContext`；面板订阅 context 变化后各自重建连接。
- **主进程已是多会话就绪**：sshService/sftpService 按 sessionId 键控、VNC bridges 是 `Map`。单会话限制完全在渲染层。
- 已保存连接按随机 UUID 键控，**允许同一 host 存多条（不同用户名/凭据）**，无 host 唯一约束。

### 1.2 问题一根因：会话工作区没有"发起新连接"的入口

信息架构上把"扫描页"当成了发起连接的唯一场所。进入会话后，用户能做的事只有：切到另一个**已保存**目标、或关掉所有标签页被踢回首页。没有断开按钮（action 存在未接线）、没有新建入口、快捷键失效。这与 1Remote/Tabby 用户的心智模型（工作区内随时开新目标）直接冲突。用户说"只能断开回首页"——实际体验更差：连"断开"按钮都没有，只能逐个关标签。

### 1.3 问题二根因：输入框与已保存列表是两套无联动的 UI

`startScan` 不查已保存列表；输入框对"这个 IP 我保存过"毫无感知。已保存条目的单击语义反而是"填入输入框去重新扫描"，把用户往完整流程里推；直连路径藏在双击里。数据模型上同 host 可有多条身份，即使做匹配也必须处理"选哪个身份"的歧义，不能简单按 host 直连第一条。

### 1.4 对标产品的做法

| 产品 | 发起新连接 | 已保存识别 |
|------|-----------|-----------|
| 1Remote | Launcher 全局唤起，主界面标签并存，新建不关旧会话 | 输入即过滤已保存主机，回车直连 |
| Tabby | 顶栏 "+" 常驻，随时新开 profile / quick connect | profiles 可搜索，选中即连 |
| Termius | Quick Connect 栏常驻主界面 | 输入匹配已保存 host 时优先展示已保存身份 |
| electerm | 顶部常驻地址栏 + 书签侧栏 | 书签点击即开新标签 |

共性三条：**发起入口常驻工作区**；**输入即匹配已保存身份**；**直连是主路径、重新探测是次路径**（探测结果可能过时，但 95% 的情况没变，不应让 95% 的用户为 5% 的协议变化付全流程成本）。

## 2. v1 方案（最小改动，保持单会话模型）

前提约束：单会话模型不变——连接新目标 = 替换当前会话。凡触发替换的操作，一律先弹确认「连接新目标将断开与 {当前 target} 的会话」，把架构限制如实告诉用户，不做隐性断开。

### A. 会话工作区常驻"新建连接"入口（解决问题一）

**A1. sider 加主按钮。** `SessionPage` sider 顶部（"已保存连接"标题上方）加 `type="primary"` 块级按钮「+ 新建连接」，`id="new-connection-button"`。

**A2. 新建连接模态（新组件 `NewConnectionModal`）。** 点击后弹 Modal，把扫描页的三步流程搬进模态，不离开工作区：

- 第一步：地址输入（`id="quick-target-input"`，**不复用** `#target-address-input`——那是冒烟契约）+「开始扫描」按钮；模态内同时列出已保存连接，点击即直连（与 sider 现有 `switchToSaved` 同逻辑）。
- 第二步：扫描完成后展示协议卡片网格（复用 `ProtocolCard` 与 store 的 `startScan`/`toggleProtocol`，逻辑零新增）。
- 第三步：底部「连接」→ 复用现有 `CredentialsModal`（组件原样，`onSubmit` 接 `beginSession`）。
- 扫描中：输入与按钮禁用、Spin+耗时；扫描失败：模态内 inline `Alert` + toast。
- 提交前确认：`session.context` 非空时 `Modal.confirm`：「连接新目标将断开与 {target} 的当前会话」（i18n `session.switchNewConfirm`），确认后走现有 `beginSession`。取消则模态留在原地，当前会话不受影响。

**A3. 断开按钮接线。** sider 底部加「断开连接」按钮（`id="disconnect-button"`，text + danger），`Popconfirm`「断开与 {target} 的连接？」→ 调用 store 里已存在但未接线的 `disconnect()`，回扫描页。这同时修复"没有显式出口"的暗伤。

**A4. 快捷键修复。** `App.tsx` 的 ⌘K 改为按 `page` 分发：扫描页聚焦输入框（现状）；会话页打开 `NewConnectionModal`。⌘W 关标签不变。

**A5. 小修：sider 当前项过滤按 id 不按 host。** 现状用 `c.host !== target` 过滤，同 host 存多条时当前项会把所有同 host 条目一起隐藏。`beginSavedSession` 时把 savedId 记入 `SessionContext`（可选字段 `savedId`），sider 改按 id 过滤。

### B. 首页输入框识别已保存地址（解决问题二）

**B1. 实时匹配。** `ScanPage` 对 `targetAddress` 变化做规范化匹配：`trim()` + hostname 小写后，与 `savedConnections` 按 `host` 精确相等匹配。`savedConnections` 已在内存，匹配零 IPC 成本。

**B2. 唯一匹配 → 直连提示条。** 输入框正下方插入提示条（`id="quick-connect-banner"`，antd `Alert` type="info" 定制）：

- 文案：「该地址已保存为 **{name}**（{username}@{host}）」
- 主操作：按钮「直接连接」（small + primary）→ 走现有 `connectSaved(id)`，**不发起扫描**直接进入会话。
- 次操作：`type="link"`「重新扫描」→ 原 `startScan()` 流程（tooltip 说明：「目标协议可能有变化时重新探测」）。
- Enter 键：唯一匹配时 = 直接连接（对齐 launcher 心智）。

**B3. 多条匹配 → 身份选择。** 同 host 存了 N 条时，提示条改为：「该地址有 {N} 个已保存身份」+ 紧凑选择列表（每项显示 name + username），选定后「直接连接」按选中条目直连；「重新扫描」保留。默认选中第一条。

**B4. 无匹配 → 零变化。** 界面、按钮、流程与现状完全一致，这是兼容底线。

**B5. 输入即过滤列表。** 已保存列表按输入子串实时过滤（匹配 name / host / username，大小写不敏感）；清空输入恢复全列表。

**B6. 已保存条目单击语义保留 + 提示。** 单击仍填入输入框（有了 B2 提示条后，它自然变成"查看/重新扫描"入口），双击与「连接」按钮直连不变；给条目加 `title="双击直接连接"` 提示。

**B7. 保存去重。** 凭据弹窗勾选保存时，若已存在同 `host`+`username` 的条目，`save` 传该条目 `id` 走更新分支，不产生重复条目；该场景下勾选框文案变为「更新已保存的连接与凭据」（`credentials.updateThis`）。

### C. 连接失败补救闭环（边界情况，可作为 v1.1）

**C1.** 从已保存连接进入会话后凭据已失效（面板报 `AUTH_FAILED`）：终端/文件/VNC 错误浮层加「重新输入凭据」按钮 → 打开 `CredentialsModal`（预填 username）→ 提交后按 id 更新该 saved 条目并用新凭据重连。若需严格控制 v1 范围，此点可延后，但建议在终端面板至少落地。

### 边界情况汇总

| 场景 | 行为 |
|------|------|
| 扫描中输入变化 | 输入框禁用（现状），提示条不更新；扫描结束后按当前输入重新匹配 |
| 扫描失败 | 模态/首页内 inline 错误 + toast（现状逻辑），提示条状态保留 |
| 连接失败（AUTH_FAILED） | 面板错误浮层（现状）+ C1 的「重新输入凭据」 |
| 同名同 host 多凭据 | B3 身份选择；保存侧 B7 按 host+username 去重更新 |
| 输入仅子串匹配（非全等） | 不出直连提示条，只过滤列表（B5），避免误判 |
| 已保存列表未加载完成 | `loaded=false` 时不做匹配，避免闪烁 |
| 会话页新建连接时当前有活动会话 | A2 确认弹窗；取消零副作用 |

### 新增 i18n 文案（zh-CN / en-US 同步添加，此处只列 zh-CN）

```
session.newConnection   + 新建连接
session.disconnect      断开连接
session.disconnectConfirm  断开与 {target} 的连接？
session.switchNewConfirm   连接新目标将断开与 {target} 的当前会话
scan.savedMatch         该地址已保存为「{name}」（{username}@{host}）
scan.savedMatchMulti    该地址有 {count} 个已保存身份
scan.directConnect      直接连接
scan.rescan             重新扫描
saved.doubleClickHint   双击直接连接
credentials.updateThis  更新已保存的连接与凭据
credentials.reenter     重新输入凭据
```

冒烟测试契约（`#target-address-input`、`#cred-username`/`#cred-password`/`#connect-submit`/`#cred-save-connection`/`#cred-save-name`、`.saved-conn-item`、`.saved-conn-delete`、`.scan-footer button`、「开始扫描」按钮名）一律不动；新增元素用新 id。

## 3. v2 展望（方向性，后续单独立项）

多目标并行会话：渲染层把单例 `SessionContext` 改为按 sessionId 索引的会话注册表（`sessions: Record<id, { context, tabs, activeTab }>`），terminal / files / vnc 三个面板 store 同样按 sessionId 实例化并各自持有连接句柄（主进程 sshService/sftpService/VNC bridges 已按 sessionId 键控，基本零改动）；sider 演进为"活动会话区 + 已保存区"，会话间切换只是改 activeSessionId 而不断连，标签页带上目标标识，扫描页退化为 Launcher 式新建连接浮层。属较大渲染层重构，建议 v1 交互经用户验证后再启动。

## 4. 验收标准（每条可独立验证）

**A（会话内新建连接）**

- A1：会话页 sider 可见「+ 新建连接」；点击弹出模态，当前会话不受任何影响（终端面板内容、连接状态不变）。
- A2：模态内完成 输入地址→扫描→选协议→凭据→连接 后，工作区切换到新目标：sider 当前项 host 更新为新地址，旧会话资源释放。
- A3：存在活动会话时触发切换，必先出现确认弹窗「连接新目标将断开与 {target} 的当前会话」；点取消则停留在原会话，无任何状态变化。
- A4：sider「断开连接」经 Popconfirm 确认后回到扫描页（`page==='scan'`、`session.context===null`）；取消则不断开。
- A5：会话页按 ⌘K/Ctrl+K 打开新建连接模态；扫描页按 ⌘K/Ctrl+K 聚焦地址输入框（现状不回归）。
- A6：同 host 存两条已保存连接时，sider 中当前项正确显示当前身份，非当前的那条仍然可见可点。

**B（已保存地址识别）**

- B1：输入与某条已保存 host 完全相同时（首尾空格不敏感），无需扫描即出现直连提示条，含连接名称与 `username@host`；点击「直接连接」不发起 scan IPC、直接进入会话页。
- B2：提示条中点「重新扫描」执行原扫描流程并正常展示协议卡片。
- B3：同 host 两条已保存连接时，提示条提供身份选择；选定后直连使用该条目的凭据。
- B4：输入无任何匹配时，扫描按钮与全流程与现状一致；`npm run smoke` 五个脚本全绿。
- B5：输入子串时已保存列表实时过滤（name/host/username 任一命中即保留）；清空输入恢复全列表。
- B6：唯一匹配时按 Enter 直接连接；不匹配时按 Enter 触发扫描（现状）。
- B7：对已存在的 host+username 再次勾选保存并连接后，已保存列表条目数不增加，且凭据已被更新（用新凭据直连可成功）。

**C（补救闭环，如纳入 v1）**

- C1：已保存凭据失效导致面板 AUTH_FAILED 时，错误浮层出现「重新输入凭据」；提交新凭据后重连成功，且对应 saved 条目已更新。
