# FleetDeck — 实现计划

> 状态:已实现(Phase 0-6 全部完成)。本文档是设计真源;运行/验证细节见 `AGENTS.md`,部署见 `docs/DEPLOYMENT.md`,路线图见 `ROADMAP.md`。

## 当前状态速览

**已实现**(与 ROADMAP.md 的 Shipped 一致):协议单一真源 + schema/TS 生成、goose 迁移 10 版、hub(REST + 双 WS + node 认证 + 离线级联)、多用户认证(admin/user 角色 + bcrypt + Bearer 会话 + 用户↔节点绑定与子树继承,见 §3.6)、node daemon、node 离线事件缓冲(`FLEETDECK_EVENT_BUFFER_DIR`,WAL 落盘 + FIFO 重放,见 §3.7)、Redis 读缓存(`FLEETDECK_REDIS_URL`,token/任务列表,显式失效 + TTL 兜底,见 §3.8)、fake + ACP 共享 adapter(codex / claude / opencode / pi)、4 状态看板 + todo 单向镜像、实时会话(流式/历史/断线补齐/直链)、权限结构化选择器、fs workspace 沙箱、任务标题模糊搜索(pg_trgm)、PATCH 状态机、任务生命周期(待办/启动、归档、完成会话可续跑、完成门,见 §3.10)、MCP 自查询工具(6 个)、Token 统计(分模型/汇总)、移动端适配、任务通知(见 §3.11)、无障碍(键盘可达 + 双主题)、Web 多语言(en + zh-CN,见 §3.9)、Docker Compose 部署(含 Traefik)、测试 123 Go + 90 web。

**未实现**(详见 §5):子任务反向写回 agent、ACP `terminal/*` 与 elicitation、`task.cancelled_at` 取消审计列;另:自定义 pi bridge 为**弃用方案**(由 `pi-acp` 取代,见 §3.4)。

## 1. 目标

一个 Web 控制面,用来管理分布在多台机器(node)上的 AI agent 会话:

- 每个 node 有多个工作空间(workspace)
- 每个工作空间是一个看板(board)
- 看板上的任务是「一次 agent session」,有 4 种状态:等待中 / 运行中 / 完成 / 等待用户响应
- 任务有多个子任务(subtask),子任务 = 该 agent session 的 todo,实时同步
- 实时看到任务进度,点开任务能进入会话(聊天、流式输出、可回复)
- 支持多种 agent:pi-agent、codex、opencode、claude 等(通过 adapter 抽象)

## 2. 架构总览

```
             ┌────────────────────────────────────────────┐
             │  Hub (Go :7710)                            │  控制面:唯一真源
             │  ├─ 嵌入式 Web UI(React + antd + Refine)  │  看板 / 会话 / 权限选择器
             │  ├─ HTTP API + WS(web 订阅)               │
             │  ├─ WS(node 注册/命令/事件)                │
             │  └─ Postgres (fleetdeck)                   │
             └──────┬──────────────────────┬──────────────┘
                    │ WS(出站)             │ WS(出站)
          ┌─────────▼────┐        ┌────────▼────────┐
          │ Node(本地)   │        │ Node(本地)      │
          │ + ACP adapter│        │ + fake adapter  │
          │ (codex/claude│        │   (测试用)      │
          │  /opencode/π)│        └─────────────────┘
          └──────────────┘
             MCP server(cmd/fleetdeck-mcp,stdio 二进制)
             —— 由 agent 在本机拉起,经 hub REST 自查询,不监听端口

  开发态另起 vite dev server(:7711),/api 与 /ws 代理到 hub;
  部署态 UI 由 hub 直接服务(同一端口/同源),SPA 也可独立部署到任意静态服务器。
```

**hub-and-spoke**:hub 是唯一真源(Postgres)。node 是跑在目标机器上的 daemon,通过 WS 连到 hub。web 只与 hub 通信,不直接接触 node。这样任意数量的 node 都能被同一个 hub 管理。MCP server 是 stdio 程序(无 `:7717` 服务),由 agent 自己拉起。

## 3. 核心设计决策

### 3.1 技术栈

| 层       | 选择                      | 说明                                |
| -------- | ------------------------- | ----------------------------------- |
| 后端     | Go 1.27                   | hub + node 都是 Go                  |
| 前端     | React + TypeScript + Vite | 看板 UI                             |
| 数据库   | Postgres                  | goose 迁移,sqlc 生成查询            |
| 实时     | WebSocket                 | hub↔web、hub↔node 两条 WS 通道      |
| 协议     | Go struct 单一真源        | 导出 JSON Schema → web 生成 TS 类型 |
| 进程管理 | devenv + process-compose  | `devenv up` 起 hub/node/web         |

### 3.2 数据模型

```
node        (id, name, host, status, capabilities, token_hash, last_seen_at)
workspace   (id, node_id, name, path, created_at)          -- 1:1 看板,不另设 board 表
task        (id, workspace_id, title, prompt, status, agent_type,
             session_id?, sort_order, created_at, updated_at)
subtask     (id, task_id, title, status, external_id?, sort_order)  -- external_id = agent todo id
session     (id, task_id, node_id, agent_type, external_session_id?, status, created_at, ended_at)
message     (id, session_id, role, content, created_at)     -- 会话历史,支持「点开进会话」
event       (id, session_id, type, payload_json, seq, ts)   -- 事件流,支持重放/断线补齐
```

**任务状态机**(4 状态,固定):

```
waiting ──start──▶ running ──agent_done──▶ done
                       │
                       ├──agent_requests_input──▶ awaiting_user
                       │                                │
                       └────◀──user_replies(send_msg)────┘
取消:running / awaiting_user ──cancel──▶ done(取消审计列 cancelled_at 未建,见 §5)
```

手动状态修改(`PATCH /api/tasks/{id}`)遵循同一状态机的**迁移表**:done 为终态、同状态幂等、非法迁移 409(`canTransitionTask`);session→task 的内部派生走自己的映射,不经过该表。

**任务标题模糊搜索**(pg_trgm):`task.title` 上建 GIN trigram 索引(`00008_task_title_search.sql`),`GET /api/tasks?...&q=term` 做子串 OR 三元相似度匹配、按相似度排序(容错打字错误);看板搜索框即接此端点。

### 3.3 协议(Protocol)

`internal/protocol` 里的 Go struct 是**唯一真源**。`cmd/fleetctl schema export` 导出 JSON Schema,`web/` 再生成 TS 类型。三端(hub/node/web)共享同一套消息形状。

- **Hub ↔ Web(订阅/推送)**:事件类型如 `task.created` / `task.status_changed` / `subtask.updated` / `session.delta` / `session.user_input_requested` / `node.status_changed`。
- **Hub ↔ Node(命令/上报)**:hub 下发 `task.start` / `session.send_message` / `session.cancel`;node 上报文本增量、todo 更新、状态变更、请求用户输入。
- **node 认证**:node 首次注册拿 token(存 `.devenv/state/fleetdeck/node-token`),后续连接携带 token。

### 3.4 Adapter 抽象(多 agent 的关键)

统一接口,把不同 agent 归一化:

```go
type Adapter interface {
    Start(ctx, StartRequest) (SessionHandle, error)
    SendMessage(ctx, sessionID, message string) error   // awaiting_user → running
    Cancel(ctx, sessionID) error
    // 事件通过回调/channel 流出:TextDelta / TodoUpdate / StateChange / UserInputRequested / Done / Error
}
```

- **ACP(Agent Client Protocol)**:codex、claude(`npx` 启动器)、opencode(自带 `opencode acp`)、pi(`npx pi-acp`)全部走**同一个 ACP adapter**(`internal/adapters/acp/`),只是启动命令不同。
- **pi**:经 `pi-acp` 包装后走上述共享 ACP adapter。早期计划的自定义 bridge(`integrations/pi/`)**未实施**——`pi-acp` 覆盖了同样的 RPC 翻译面,无维护两份的必要(若未来弃用 pi-acp 再重启,见 §5)。
- **fake adapter**:模拟 agent,产出 todo、推进状态、可请求输入。用于 UI 开发与集成测试,不碰真 agent。

**todo → subtask 同步**:adapter 的 `TodoUpdate` 事件携带带 id 的 todo 列表;node 按 `external_id` 与现有 subtask 做 diff(增/改/删),写回 hub。方向是**单向镜像**(session → 看板),即用户需求里「session 的 todo 就是任务的子任务」。

**permission 暂停 + 结构化选择**:ACP 的 `session/request_permission` 暂停回合,`UserInputRequested` 的 prompt 携带 `[permission_request] options=[{"id":…,"label":…}]`;web 端解析后渲染选择器(`web/src/permission.ts` + `PermissionPicker`),用户点选后经 `POST /api/sessions/{id}/send` 回传 `[permission_response] option_id="…"`(格式由 adapter 正则解包,不可漂移;解包失败或 option_id 不在已知集合按拒绝处理,永不自动批准)。

**fs 能力协商**:ACP adapter 只广告已实现的能力 —— `fs/read_text_file` / `fs/write_text_file`(workspace 沙箱,symlink 解析后做包含检查);`terminal/*` 与 elicitation 不广告、调用即拒(广告了不实现会让 agent 挂死)。终态信号(`Done`/`Error`)携带最终状态,adapter 不再配对终态 `StateChange`。

### 3.5 目录结构

```
cmd/hub/                 hub 入口
cmd/node/                node daemon 入口
cmd/fleetctl/            CLI(schema export 等)
cmd/fleetdeck-mcp/       MCP server 入口(stdio,由 agent 拉起)
internal/protocol/       Go struct 单一真源(含 transport.go WS 信封)
internal/store/          sqlc 查询 + Postgres 访问
internal/hub/            hub 业务逻辑(API、WS、事件分发)
internal/node/           node 逻辑(注册、会话生命周期)
internal/adapters/       Adapter/EventSink 接口 + fake / acp / all 注册
internal/mcp/            MCP server 工具实现
migrations/              goose SQL 迁移
web/                     React 前端(antd + Refine)
deploy/                  Docker Compose 部署件(Caddyfile、Traefik 路由、node 模板)
scripts/                 smoke 脚本
examples/                node.config.yaml / fake-node.config.yaml / MCP 配置
docs/                    本文档 + DEPLOYMENT.md
Dockerfile / docker-compose.yml / README*.md / ROADMAP.md / AGENTS.md
```

### 3.6 多用户认证与权限

- **账号**:`users` 表(email 唯一、小写去空白,bcrypt 密码,`role ∈ {admin,user}`)。账号由 admin 创建,**无自助注册**;首启 `POST /api/auth/setup` 建首个 admin(仅当 users 表为空,否则 409)。
- **会话**:`auth_tokens` 存 sha256(token);token 为 32 字节随机 base64url,明文只回一次,TTL 30 天,`revoked_at` 可吊销。HTTP 用 `Authorization: Bearer`,`/ws/web` 用 `?token=`(浏览器 WebSocket 无法带 header);`/ws/node` 不变(node token 注册帧)。
- **权限**:两档角色 `admin`(系统管理员,全权)/ `user`;授权模型是**用户↔节点绑定**(`node_members`,在用户页由 admin 绑定)+ 子树继承——绑定用户可见/可写该节点下全部 workspace/task/session/message/event。不可见的读返回 404(不泄露存在性),越权写返回 403;非 admin 的节点列表/详情只含已绑定节点。workspace/task 不单独设成员表,归属沿子树自动下传。
- **实现**:`internal/hub/{middleware,auth_handlers}.go`;MCP 客户端经 `FLEETDECK_HUB_TOKEN` 环境变量带 Bearer。认证属 HTTP 层,不进 `internal/protocol`(node/hub wire 契约不变)。

### 3.7 node 离线事件缓冲

- **启用**:`FLEETDECK_EVENT_BUFFER_DIR` 设置时启用;未设置保持旧行为(断线即丢)。
- **WAL 队列**(`internal/node/buffer.go`):每个出站信封一个 JSON 文件(`%020d.json`,原子 tmp+rename),序号续接磁盘最大值(重启不乱序);writer 是唯一消费者,按序号发送、**成功写入 socket 才删除文件**。
- **语义**:严格 FIFO、at-least-once(崩溃/断线后未删文件会重发);坏文件自动跳过删除,不阻塞队列。adapter 会话的 ctx 是进程级,跨断线存活,故运行中会话的全部事件都经此路径重放。

### 3.8 Redis 读缓存

- **接入**:`FLEETDECK_REDIS_URL` 未设置 → Noop 缓存(直连 DB);Redis 故障一律按 miss 处理,请求永不因缓存失败。devenv(`services.redis`,:56379)与 docker-compose(`redis:7-alpine`)各带一个 Redis。
- **缓存面**(cache-aside,`internal/cache`):①token→user 鉴权解析(TTL 60s,logout 显式删除,TTL 封顶批量吊销窗口)②工作区任务列表 `tasks:ws:<id>`(TTL 30s;create/patch/dispatch/会话推导/离线级联全部经 `invalidateTasks` 显式失效)。模糊搜索与 node 级列表(带成员过滤)刻意不缓存。
- **测试**:miniredis 提供真实 Redis 语义;`newTestHub` 默认挂缓存,整套 hub 测试都穿过缓存层(失效回归 + 登出吊销 + 故障降级)。

### 3.9 Web 多语言

- **框架**:react-i18next,语言 `en` / `zh-CN`;按界面分命名空间 `common`(状态/动作/相对时间等共享原子)、`nav`、`auth`、`pages`、`board`、`session`。组件用 `useTranslation('<ns>')`,非组件代码用 `i18n.t('<ns>:key')`。
- **约定**:英文文案为规范值(测试与截图按其固定),新增键必须同时补两种语言;`util.ts` 的 `statusLabel`/`formatRelative` 已本地化,直接复用。
- **切换**:Header 与登录卡内置 `LanguageSwitcher`,持久化 `localStorage('fleetdeck.lang')`,首访按 `navigator.language`;`<html lang>` 同步;antd 内置文案经 `AntdProvider` 的 locale 包切换。

### 3.10 任务生命周期(续跑 / 完成门 / 待办 / 归档)

- **续跑**:对 `done`/`failed` 会话发消息(`POST /api/sessions/{id}/send`)即重开——会话→`running`、任务经 `moveTaskTo` 重开 `running`(手动 PATCH 表保持 `done` 终态不变),hub 下发 `MsgSessionResume`,node 以 `session/load` 重挂 agent 侧会话续跑;无法重挂(external id 缺失/未知会话)则**以该追问为 prompt 开新会话**兜底,保证用户消息必有回应。
- **完成门**:会话自然结束(`end_turn`)时,若镜像子任务仍有未完成项,任务停靠 `awaiting_user` 而非 `done`(零子任务或全完成则照旧);`failed` 仍收敛 `done`。
- **待办任务**:`POST /api/tasks` 带 `auto_start:false` 则不派发、停在 `waiting`(卡片带"待办"徽章);`POST /api/tasks/{id}/start` 手动派发(已启动/非 waiting → 409;节点离线 → 202 停 waiting)。
- **归档**:`PATCH {archived:true}` 仅限 `done` 任务(否则 409),列表默认排除、`?archived=1` 只看归档;取消归档随时可用。

### 3.11 Token 统计 / 移动端 / 通知

- **Token 统计**:adapter 每轮上报 `EventSink.Usage`(ACP `session/prompt` 结果的 `usage` 字段:inputTokens/outputTokens/totalTokens;model 取 `session/new` configOptions 的 `model` 当前值,缺省回落 `agent_type`,无 usage 则不落行)→ `EvSessionUsage` → `token_usage` 表;`GET /api/stats/tokens` 出分模型与汇总(按节点绑定裁剪可见性),用量页 + 会话侧栏展示。
- **任务统计 / 仪表盘**:`GET /api/stats/tasks` 按窗口统计 `created`/`completed` 及当日任务的 `by_status` 分布(过滤/可见性与 `GET /api/stats/tokens` 同契约)。`/` 为仪表盘(今日任务 + Token 统计),节点列表在 `/nodes`。
- **管理后台布局**:`web/src/components/AppLayout.tsx` 提供左侧菜单栏(可折叠为图标栏;<768px 收为顶部汉堡 + 抽屉菜单)。菜单图标必须转发 antd 注入的 `className`/`style`(`cloneElement` 会加 `ant-menu-item-icon`,折叠态靠 `ant-menu-item-icon + span` 隐藏文字)。
- **移动端**:<768px 单列布局(看板列纵向堆叠、会话侧栏下沉、弹窗收缩、粗指针触控目标 ≥36px),320px 起可用。
- **通知**:`TaskNotificationBridge` 订阅 WS 的 `task.created`/`task.status_changed` → 浏览器通知(权限 + `localStorage('fleetdeck.notify')==='on'` 双门控,按任务 tag 合并,点击聚焦标签页)。

## 4. 阶段计划(✅ 已全部完成)

依赖关系:`P0 → P1 → P2 → P3 → P4` 为主线;`P5`、`P6` 依赖 `P3`(adapter 接口)后可并行。各阶段产出与验收均已落地;与原计划的实质偏差集中在 Phase 5:**没有独立的 codex adapter 和 pi bridge** —— codex/pi 均归一到共享 ACP adapter(§3.4),真实验收含 opencode 端到端 smoke。

### Phase 0 — 骨架 + 协议

- **目标**:repo 能编译,协议类型三端一致。
- **产出**:`go.mod`、目录骨架、`internal/protocol` 全部 Go struct、`cmd/fleetctl schema export`、`web/` 初始化 + `gen:types` 脚本。
- **验收**:`just gen` 干净跑通,web 下生成 `src/gen/protocol` TS 类型。
- **依赖**:无。

### Phase 1 — 数据库

- **目标**:数据模型落地,查询可用。
- **产出**:goose migrations(node/workspace/task/subtask/session/message/event)、sqlc 配置与生成查询。
- **验收**:`migrate up` 干净、`db-reset` 可重建、`go test` 通过。
- **依赖**:P0。

### Phase 2 — Hub 核心

- **目标**:hub 能接 node、能推 web。
- **产出**:HTTP API(CRUD node/workspace/task)、两条 WS 端点、node 认证、事件写入 store 并广播给 web。
- **验收**:hub 启动、`/api/health` 通、可注册一个假 node 并收到广播。
- **依赖**:P1。

### Phase 3 — Node + Adapter 接口 + fake adapter

- **目标**:跑通「任务 = session」的完整生命周期,不碰真 agent。
- **产出**:`cmd/node`(读 YAML 配置、连 hub、注册)、Adapter 接口、fake adapter(产出 todo、推进状态、可请求输入)。
- **验收**:`devenv up fake-node` 后,一个任务走完 `waiting→running→done`,todo 实时生成。
- **依赖**:P2。

### Phase 4 — Web 看板 UI

- **目标**:可视化 + 实时 + 会话。
- **产出**:node 列表、工作空间/看板视图、4 列看板、任务卡(含子任务)、实时 WS 更新、会话视图(点任务进入、流式输出、`awaiting_user` 时可回复)。
- **验收**:用 fake-node 在 UI 上完成「建任务→看状态流转→点开会话→回复→完成」全流程。
- **依赖**:P3。

### Phase 5 — 真实 adapter

- **目标**:接真 agent。
- **产出**:pi bridge、ACP adapter(codex/claude/opencode)、codex adapter。
- **验收**:`probe-adapters` 报告可用 agent,真 agent 驱动一个任务跑通。
- **依赖**:P3(接口);可与 P4 并行。

### Phase 6 — MCP server

- **目标**:agent 能自查询自身任务/session 状态。
- **产出**:`fleetdeck_*` MCP 工具。
- **验收**:agent 通过 MCP 查询到自己的任务与子任务状态。
- **依赖**:P3(可与 P4/P5 并行)。

## 5. 未实现(Deferred)

以下均为**刻意不做**,附重新启动的触发条件(与 `ROADMAP.md` 的 Later 表同源;该表另含测试覆盖扩展、多 hub 等条目):

- **子任务反向写回 agent**(现为 session→看板单向镜像)——触发:产品定下反向语义(ACP 无标准方法)
- **ACP `terminal/*` 与 elicitation**(现不广告、调用即拒)——触发:出现必须宿主终端的 agent,且先定沙箱威胁模型
- **`task.cancelled_at` 取消审计列**(现取消直接进 done,无审计痕迹)——触发:合规/审计需求
- **自定义 pi bridge** —— **弃用方案**,由 `pi-acp` 取代(§3.4);仅当 pi-acp 能力不足时重启

## 6. 约定

- 协议 Go struct 是唯一真源,改协议只改 `internal/protocol`,再 `go run ./cmd/fleetctl schema export` + `cd web && pnpm gen:types` 同步 web 类型。
- 所有数据库访问走 sqlc;新增表先写 migration。
- 新 adapter 必须实现 `internal/adapters` 的接口,并在 `probe-adapters` 中可探测。
- 状态流转只经 hub 落库后广播,node 不直接改看板状态。
- hub 对任意 origin 放行(WS `CheckOrigin` + HTTP CORS 头不变),但 `/api/*` 与 `/ws/web` 均需 Bearer 认证(§3.6;`/api/health`、`/api/auth/*` 的登录/setup、SPA 静态资源除外)。开发态 vite(:7711)把 `/api`、`/ws` 代理到 hub(:7710);部署态 UI 由 hub 内嵌服务(同源),SPA 也可经 `web/public/config.js` 指定接口域名后独立部署到静态服务器。
