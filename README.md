# LeeLens Backend

> LeeLens 的 Go + Gin + Eino 后端服务，纯 API server。前端（Next.js）位于 [`../eino-lee-lens`](../eino-lee-lens)，独立部署。

## 技术栈

- **语言**：Go 1.24+
- **Web 框架**：Gin
- **数据库**：SQLite（默认）/ MySQL
- **ORM**：GORM
- **日志**：klog
- **AI 编排**：Eino ADK
- **热重载**：Air

## 快速开始

### 环境要求

- Go 1.24+
- `make`（Windows 可用 Git Bash + choco install make）

### 安装 + 启动

```bash
# 1. 安装依赖 + air
make setup

# 2. 生成配置文件
make init-config

# 3. 编辑 config.yaml，填入 LLM API Key
#    或设置环境变量 OPENAI_API_KEY / OPENAI_BASE_URL / OPENAI_MODEL_NAME

# 4. 开发模式（任选其一）
make dev       # go run 方式
make air       # air 热重载（Linux/macOS 推荐）
```

默认监听 **http://localhost:8080**。

### 生产构建

```bash
make build          # 当前平台
make build-linux    # Linux amd64 + arm64
make build-all      # 全平台（Linux/macOS/Windows × amd64/arm64）
```

二进制在 `bin/`，首次运行会自释放 agents YAML 到 `./agents/`。

## 主要端点

- `GET  /api/repositories` — 仓库列表
- `POST /api/repositories` — 添加仓库
- `GET  /api/documents/:id` — 文档内容
- `WS   /api/repositories/:id/chat/sessions/:sid/stream` — AI 对话流
- `GET/POST  /mcp/streamable` — MCP Server（Streamable HTTP）

## 目录结构

```
cmd/server/          启动入口
config/              配置加载
agents/              Agent YAML 定义（热加载）
internal/
  domain/            领域层：writers（api/db/toc/doc...）
  service/           服务层：repository / task / document / chat / sync / ...
  repository/        数据访问层
  handler/           HTTP handler
  mcp/               MCP Server
  pkg/               公共工具（database / git / adkagents）
  router/            路由
  eventbus/          事件总线
docs/                需求文档 / 设计文档 / 测试规范
```

## 部署

`fly.toml` 是 Fly.io 部署配置，`Dockerfile` 可直接构建容器镜像。

## 许可证

MIT
