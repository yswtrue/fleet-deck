# FleetDeck

[English](README.md) | **简体中文**

> 多机 AI agent 会话的 Web 控制面。Hub(Go)↔ 本地 Node daemon(Go)↔ Web UI(React + antd + Refine),Postgres(sqlc)持久化。

## 解决什么问题

现在用 AI coding agent(OpenCode、Codex、Claude、pi……)干活,基本是"一个终端一个会话"的模式:会话散落在多台机器、多个终端里,没有统一的地方看"现在有哪些 agent 在跑、跑到哪了、有什么在等我回复"。更要命的是三件事:

1. **看不见**:agent 的计划(todo)、输出流、遇到的问题都锁在各自的终端里,离开那台机器就失联;
2. **等不起**:agent 跑到一半需要权限批准(执行命令/改文件)或提问时,如果人不在终端前,会话就干等着;
3. **管不了**:批量任务、跨机器调度、事后复盘,都得一台台 SSH 上去翻。

FleetDeck 把这些会话收拢到一个 Web 控制面:

- **看板视角**:node(机器)→ 工作空间(看板)→ 任务(一次 agent 会话,4 状态)→ 子任务(agent 的 todo 实时镜像),人不在终端也能看到全局;
- **会话视角**:点开任意任务进入聊天式会话视图 —— 流式输出、完整历史、随时回复;
- **不停摆**:agent 请求权限时,Web 端弹出**结构化选择器**(Allow / Deny……)一键答复;agent 提问时同理。回复经 hub 送回 agent,会话继续;
- **多 agent 统一接入**:OpenCode / Codex / Claude / pi 共用一个 ACP adapter 抽象,加新 agent 只需注册一个启动配置;
- **agent 自查询**:内置 MCP server(`fleetdeck_*` 工具),agent 可以查自己的任务/子任务/会话状态,或恢复挂起的会话。

## 架构

```
             ┌──────────────────────────────┐
             │  Web UI (React + Vite)        │  看板 / 会话 / 权限选择器
             └──────────────┬───────────────┘
                            │ HTTP + WebSocket(实时推送 + 断线补齐)
             ┌──────────────▼───────────────┐
             │  Hub (Go)                     │  控制面:唯一 DB 写入方
             │  ├─ REST API + WS 广播        │
             │  ├─ node WS(注册/命令)         │
             │  └─ Postgres                  │
             └──────┬───────────────┬────────┘
                    │ WS(出站)      │ WS(出站)
          ┌─────────▼────┐   ┌──────▼─────────┐
          │ Node(本地)   │   │ Node(本地)     │   …每台目标机器一个
          │ + ACP adapter│   │ + fake adapter │   (agent 装在本机)
          └──────────────┘   └────────────────┘
```

- **hub-and-spoke**:web 只与 hub 通信;node 是目标机器上的薄 daemon,**主动出站**连 hub —— node 机器不需要开任何入站端口。
- **单一协议真源**:`internal/protocol/` 的 Go struct → JSON Schema → web 端 TS 类型,三端消息形状不会漂移。
- **事件流**:会话输出、todo 镜像、状态流转都走 WS 实时推送,并落 `event` 表支持断线重连补齐。

## 核心能力速览

| 能力         | 说明                                                                                                               |
| ------------ | ------------------------------------------------------------------------------------------------------------------ |
| 4 状态看板   | waiting / running / done / awaiting_user,子任务 = agent todo 单向镜像                                              |
| 实时会话     | 流式输出、消息历史、`awaiting_user` 时直接回复                                                                     |
| 权限选择器   | 结构化选项、精确的 `option_id` wire 格式,永不自动批准                                                              |
| 标题模糊搜索 | pg_trgm 三元相似度,容错打字错误,按匹配度排序                                                                       |
| 多机管理     | node 注册/在线状态、离线级联                                                                                       |
| MCP 自查询   | `fleetdeck_session_status` / `task_list` / `subtask_list` / `message_tail` / `event_tail` / `session_send_message` |
| ACP 文件沙箱 | agent 只能读写自己工作空间内的文件                                                                                 |

## 快速开始

### 控制面(Docker Compose)

```sh
cp .env.example .env        # 端口 + 数据库凭证都在这里,按需修改
docker compose up -d --build
# 单端口全搞定(hub 内嵌 UI + API + WS):
#   http://localhost:<FLEETDECK_HUB_PORT>   (.env 默认值:7710)

# 可选:Traefik 入口(:80/:443,label 自动发现路由):
docker compose --profile traefik up -d
```

### Node(本地跑,每台装了 agent 的机器)

```sh
go build -o fleetdeck-node ./cmd/node
./fleetdeck-node config --config node.config.yaml   # 选择要提供的 agent
# 首次启动:--hub-url ws://<hub>:7710/ws/node --join-token <UI 里生成的令牌>;
# 注册成功后 hub_url 会写入配置文件,之后只需指定配置文件
./fleetdeck-node --config node.config.yaml   # 模板:deploy/node.config.example.yaml
```

### 开发环境(devenv)

```sh
cp .env.example .env    # 可选:自定义端口/凭证(默认值即可用)
devenv shell
devenv up                          # hub + node + web,连本地 Postgres
devenv up hub node web fake-node   # 可选:在上面基础上再加一个确定性的 fake agent node
```

完整部署指南:**[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)**(英文)。

## 开发与验证

```sh
# 后端
go build ./... && go vet ./...
gofmt -l cmd/ internal/        # 必须为空
go test -race ./...            # 78 passed / 15 packages(需 live Postgres: $FLEETDECK_DATABASE_URL,devenv shell 从 .env 派生)

# 前端(cd web)
pnpm tsc --noEmit && pnpm lint
pnpm test                      # vitest
pnpm build
```

## 路线图

已实现与刻意缓建的清单(含启动条件):**[ROADMAP.md](ROADMAP.md)**(英文)。

## 更多文档

- **[docs/DEPLOYMENT.md](docs/DEPLOYMENT.md)** —— 部署指南
- **[docs/IMPLEMENTATION.md](docs/IMPLEMENTATION.md)** —— 设计真源:数据模型、协议、adapter 抽象
- **[AGENTS.md](AGENTS.md)** —— 工具链、验证命令、架构不变量、陷阱清单
