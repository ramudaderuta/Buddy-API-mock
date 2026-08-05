# Buddy-API-mock

`Buddy-API-mock`（仓库名 `api-mock`）是一个仅供本机/个人使用的 OpenAI Chat
Completions 中转控制台。它通过本地账户池转发请求，只持久化加密后的账户密钥与
请求元数据，不保存提示词、明文密钥或上游响应正文。

控制台默认监听 `127.0.0.1:13100`。不要把该端口直接暴露到公网。

## 功能概览

- 标准 `POST /v1/chat/completions` 中转：原样传递客户端的 `stream` 语义，`stream:true` 透传 SSE，`stream:false` 透传标准 JSON（包括 `tool_calls`）
- 管理员密码登录的本机控制台：账户池、请求记录、API
- 调度策略：默认 **填充优先**，可切换 **轮询优先**
- 账户 Endpoint 必须为 HTTPS；本地 AES-GCM 加密保存 API Key
- 内置固定系统提示词；原生 WorkBuddy 正式请求保留其消息画像并移除内置工具声明
- WorkBuddy 的 `conversation_topic` 阶段在本地返回固定标题 SSE，不向上游发送该阶段的提示词；正式 `conversation` 仍使用账户池转发
- 内置固定弱网策略：连接/TLS/首包分段超时、HTTP 连接池、SSE 逐块 flush、20 秒 heartbeat、180 秒上游流空闲保护，以及仅在请求尚未写出时进行的最多 10 次安全重试

## 本机直接运行

```bash
API_MOCK_ADMIN_PASSWORD='local-password' \
API_MOCK_API_KEY='local-relay-key' \
go run ./cmd/api-mock
```

打开 `http://127.0.0.1:13100`，添加 HTTPS Endpoint、API Key、模型 ID，然后向：

```text
http://127.0.0.1:13100/v1/chat/completions
```

发送标准 Chat Completions 请求。本服务会遵循并原样传递客户端的 `stream` 参数：`false`（或省略）时要求上游返回单个 `chat.completion` JSON，`true` 时透传 `chat.completion.chunk` SSE。当前验证的 WorkBuddy 上游原生支持这两种模式，因此不在本地重放或聚合响应。

## 私有 Docker 部署

发布镜像（固定 tag）：

```text
docker.io/ramudaderuta/buddy-api-mock:latest
```

面向使用者的编排文件是 [`deploy/docker-compose.yml`](deploy/docker-compose.yml)。
**只需拉镜像 + 设置管理员密码**，不必克隆源码，也不必再挂载 Skill 文件。

### 1. 准备环境文件

```bash
cd deploy
cp .env.example .env
```

编辑 `.env`：

| 变量 | 说明 |
| --- | --- |
| `API_MOCK_ADMIN_PASSWORD` | 控制台管理员密码（**必填**）。只来自环境变量，**不会**写入 data 卷。 |
| `API_MOCK_API_KEY` | 转发服务 API Key（**必填**）。客户端调用 `/v1/chat/completions` 时使用；**不是**上游账户密钥。 |
| `API_MOCK_WORKBUDDY_USER_ID` | 可选。仅转发给上游的 WorkBuddy 用户标识；保留在本机 `.env`，不要提交。 |
| `API_MOCK_PRIVATE_PROFILE_DIR` | 可选。宿主机私有画像目录；以只读方式挂载到容器 `/private-profile`。默认是 `deploy/.private/workbuddy-profile`。 |
| `API_MOCK_MODEL_INSTRUCTIONS_FILE` | 可选。容器内私有系统提示词路径。普通 agent 请求使用它构造 WorkBuddy 画像。 |
| `API_MOCK_WORKBUDDY_PROFILE_FILE` | 可选。容器内私有 WorkBuddy 请求头画像路径。普通 agent 请求使用其中白名单头。 |
| `API_MOCK_PROMPT_*` | 可选。私有 WorkBuddy 模板的动态块；仅在系统提示词文件包含对应 `{{placeholder}}` 时使用。具体变量见 `deploy/.env.example`。 |
| `API_MOCK_IMAGE` | 可选。默认 `docker.io/ramudaderuta/buddy-api-mock:latest`。 |

### 2. 启动

```bash
cd deploy
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml ps
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:13100/
```

浏览器打开 `http://127.0.0.1:13100`，用 `.env` 中的管理员密码登录。

账户密文与本地 keyring 在命名卷 `api-mock-data`。普通 `docker compose down`
**不会**删除该卷。

### 3. 为普通 Agent 配置私有 WorkBuddy 画像

只有普通 OpenAI 兼容 Agent 需要此配置；原生 WorkBuddy 请求会携带自己的系统提示词
和请求头。镜像必须包含 `API_MOCK_MODEL_INSTRUCTIONS_FILE` 与
`API_MOCK_WORKBUDDY_PROFILE_FILE` 支持。旧镜像即使设置了这两个变量也不会启用该画像。

先在你自己控制的本地环境中完成一次 WorkBuddy 正式对话，并从该请求取得以下两项。
不要将它们上传到 issue、聊天记录、Git 仓库或镜像：

1. 请求 JSON 中 `messages` 数组的 `role: "system"` 内容，保存为
   `model_instructions.private.md`。
2. 同一请求的 HTTP 请求头，保存为 `workbuddy_profile.private.json`。文件格式如下；
   可保留全部头，但必须删除 `Authorization` 和 `X-API-Key`。服务只读取其中允许的
   WorkBuddy 头，账户池的上游认证始终由服务生成。

   ```json
   {
     "headers": {
       "User-Agent": "<captured value>",
       "X-Agent-Purpose": "conversation"
     }
   }
   ```

在 `deploy/` 下直接创建宿主机私有目录并放入这两个文件：

```bash
mkdir -p .private/workbuddy-profile
# 将 model_instructions.private.md 和 workbuddy_profile.private.json 放到此目录
```

在 `deploy/.env` 设置宿主机目录和容器内读取路径：

```dotenv
API_MOCK_PRIVATE_PROFILE_DIR=./.private/workbuddy-profile
API_MOCK_MODEL_INSTRUCTIONS_FILE=/private-profile/model_instructions.private.md
API_MOCK_WORKBUDDY_PROFILE_FILE=/private-profile/workbuddy_profile.private.json
```

然后重建容器以加载新的 Compose 环境变量：

```bash
cd deploy
docker compose -f docker-compose.yml up -d
```

#### 使用未展开的 WorkBuddy 模板

不要把已验证可用的 WorkBuddy 模板替换成通用短提示词：上游可能将完整模板作为客户端
画像的一部分。模板不随本仓库或镜像分发；请在**自己的** WorkBuddy 安装资源中搜索
`workbuddy-prompt.tpl`，将原文件复制到私有目录
`.private/workbuddy-profile/workbuddy-prompt.tpl`。然后在忽略的 `deploy/.env` 设置：

```dotenv
API_MOCK_MODEL_INSTRUCTIONS_FILE=/private-profile/workbuddy-prompt.tpl
API_MOCK_PROMPT_MODEL_NAME=your-model-id
API_MOCK_PROMPT_PRODUCT_NAME=WorkBuddy
API_MOCK_PROMPT_RESPONSE_LANGUAGE=your-language
```

服务只渲染下列固定占位符，值从忽略的 `deploy/.env` 读取：

`ArtifactDirectoryPath`、`BinaryContext`、`ClawMemory_1`、`ClawMemory_2`、
`ClawMemory_3`、`dataFolderName`、`ExpertManagement`、`modelName`、
`PluginAgentPrompt`、`productName`、`ResponseLanguage`、`subAgentPrompt`、
`ToolResultPresentationPrompt`、`UserLocalMemoryContent`、`UserMemoryContent`、
`WorkingMemoryContent`。

它们分别对应 `API_MOCK_PROMPT_<UPPER_SNAKE_CASE>`，完整列表和安全占位写法见
`deploy/.env.example`。未配置的已知变量渲染为空；遇到未知变量时容器启动失败，避免将
未渲染的模板发送到上游。模板与变量值都必须保留在私有目录或忽略的 `.env`，不能提交。

普通 Agent 已声明的 `tools` 和 `tool_choice` 会原样转发；只有调用方未声明 `tools` 且私有
`API_MOCK_WORKBUDDY_TOOL_TEMPLATE_FILE` 已配置时，服务才以该模板作为 WorkBuddy 工具目录回退。

#### Pi 与其他 Agent 的工具兼容模式

需要让普通 Agent 使用 function tools 时，在该 Agent 的 OpenAI Chat Completions
provider 上设置以下请求头：

```text
X-API-Mock-WorkBuddy-Compatible: 1
```

该请求头启用 WorkBuddy 上游画像，并且是客户端请求头，不是部署服务器的 `.env` 变量。
兼容模式会移除客户端的 `system` / `developer` 消息及 Pi 专有的
`max_completion_tokens`、`store`，注入私有 WorkBuddy 系统提示词，并保留客户端的
`tools`、`tool_choice`、assistant `tool_calls` 和 `tool` 结果。因此 function 名称及参数
schema 应由客户端自己定义。Relay 根据请求体中的模型 ID，在配置了相同模型 ID 的已启用
账户中应用当前调度策略。

Pi 的 `models.json` 将 `headers` 放在 **provider**，而不是 model 项。示例中的模型 ID
和 Key 均为占位符：

```json
{
  "providers": {
    "local-workbuddy": {
      "baseUrl": "http://127.0.0.1:13100/v1",
      "api": "openai-completions",
      "apiKey": "<relay-api-key>",
      "headers": {
        "X-API-Mock-WorkBuddy-Compatible": "1"
      },
      "models": [{ "id": "<model-id>" }]
    }
  }
}
```

其他支持自定义 OpenAI Chat Completions provider 的 Agent，例如 OpenClaw，应配置相同的
base URL、relay API Key 和兼容请求头，并使用标准 function `tools`、assistant
`tool_calls` 及 `role: "tool"` 结果消息。`X-API-Mock-WorkBuddy-Compatible` 不会对
其他 provider 自动生效；每个 client/provider 都必须显式设置。

兼容模式保留 `max_completion_tokens`；如果请求同时包含旧的 `max_tokens`，只保留
`max_completion_tokens`，避免冲突。当前已验证上游原生支持 `stream:false` JSON、
`stream:true` SSE、工具选择和工具结果回填。`n > 1` 和 `logprobs` 虽会被转发，但当前
上游会静默忽略，客户端不应依赖这两项能力。

### 固定弱网策略

所有参数固化在程序中，不通过环境变量覆盖：TCP 连接 10 秒、TLS 握手 10 秒、等待响应头
180 秒、非流式完整请求 10 分钟、SSE 上游 180 秒无新字节即终止、下游 SSE 每 20 秒发送
标准注释 heartbeat。连接池最多保留 100 个空闲连接、每个上游 20 个空闲连接、每个上游
最多 50 个总连接。

仅当 HTTP tracing 证明请求尚未写给上游、且尚未收到任何响应字节时，Relay 才会对当前
选定账户执行安全重试。一个逻辑请求最多尝试 10 次，前 9 次失败后的固定退避依次为
250ms、500ms、1s、2s、4s、8s、16s、32s、60s；第 10 次仍失败时立即返回，不再等待、
切换账户或循环重试。客户端取消会立即终止等待和后续尝试。

收到任何 HTTP 响应、请求已经写出、已经向客户端发送字节、SSE error、SSE 缺少 `[DONE]`、
流中途断开或客户端取消时都不会透明重放，以避免重复计费、重复文本或重复工具副作用。
服务不维护账户失败计数、冷却、暂停调度或健康过滤；HTTP 4xx/5xx 与流式错误只记录本次
请求结果，是否发起新的逻辑请求由客户端决定。

SSE 响应会设置 `Cache-Control: no-cache, no-transform` 与 `X-Accel-Buffering: no`，每次
收到上游数据后立即 flush；heartbeat 只维护 Relay 到客户端/Cloudflare 的空闲链路，不提供
模型输出断点续传。中途断流会失败，不会拼接第二次生成。

仅实现 Chat Completions 的 function-tool 路径。OpenAI Responses、Anthropic Messages、
MCP 和没有自定义请求头能力的客户端需要独立适配。OpenClaw 尚未在此仓库环境完成真实
运行验证。

挂载为只读，文件不会复制到镜像或 `api-mock-data` 卷。Linux/WSL 宿主机应让服务账户
`65532` 可读取这两个私有文件；Windows Docker Desktop 的 bind mount 通常不需要额外调整。
仅验证容器内文件存在且运行账户可读时，可执行：

```bash
docker run --rm --user 65532:65532 \
  -v "$(pwd)/.private/workbuddy-profile:/private-profile:ro" \
  alpine:3.20 \
  sh -c 'test -r /private-profile/model_instructions.private.md && test -r /private-profile/workbuddy_profile.private.json'
```

### 4. 常用运维

```bash
cd deploy
docker compose -f docker-compose.yml logs -f --tail 100
docker compose -f docker-compose.yml restart
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml down
```

### 5. 从本仓库源码构建（维护者）

```bash
cd deploy
docker compose -f compose.yaml up -d --build
```

### 6. 卸载

```bash
cd deploy
docker compose -f docker-compose.yml down
# 危险：同时删除数据卷
# docker compose -f docker-compose.yml down -v --rmi local
```

## 调用转发 API

客户端必须携带本服务的 `API_MOCK_API_KEY`（与控制台管理员密码、上游账户 Key 都不同）。
先查询账户池中已启用账户提供的模型 ID；结果会去重并按 ID 排序：

```bash
curl http://127.0.0.1:13100/v1/models \
  -H "Authorization: Bearer $API_MOCK_API_KEY"
```

从响应的 `data` 数组中选择一个对象的 `id`，填入下方的 `<model-id>`，再发起请求：

```bash
curl http://127.0.0.1:13100/v1/chat/completions \
  -H "Authorization: Bearer $API_MOCK_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"<model-id>","messages":[{"role":"user","content":"hi"}]}'
```

Relay 只在已启用且配置了相同模型 ID 的账户中应用当前调度策略，并以账户配置的模型 ID
转发请求；没有匹配账户时返回 `503 requested model is unavailable`。

也支持 `X-API-Key: <API_MOCK_API_KEY>`。缺少或错误时返回 `401 invalid api key`。

上游真实密钥在控制台「账户池」中配置；转发时由服务自动替换请求头。

控制台「API」页面位于「请求记录」下方，提供 JSON/SSE curl 示例和测试请求表单。
已登录管理员发送测试请求时，服务器通过受 CSRF 保护的内部路由使用已配置的
`API_MOCK_API_KEY`；浏览器不会读取、接收或持久化该 Relay Key。

## 管理员密码与数据卷

- **管理员密码**：每次容器启动读取 `API_MOCK_ADMIN_PASSWORD`。改 `.env` 后执行
  `docker compose up -d` 重建；单纯 `docker restart` 不会加载新的 Compose 环境变量。
- **数据卷 `api-mock-data`**：加密账户、keyring、策略、请求元数据。
- **系统提示词**：默认编译进镜像；请求记录仍不保存提示词、密钥或上游正文。

## 隐私配置与转发边界

- `API_MOCK_ADMIN_PASSWORD`、`API_MOCK_API_KEY` 和可选的
  `API_MOCK_WORKBUDDY_USER_ID` 只配置在未提交的 `deploy/.env`。
- 上游 Endpoint 与上游 API Key 只通过本机控制台配置；API Key 使用本地 keyring
  加密后写入 `api-mock-data`，不会写入源码、Compose 或 Git。
- WorkBuddy 的 `conversation_topic` 请求在本地生成固定标题，不把该阶段的提示词
  转发给上游。
- 正式 `conversation` 请求作为中转功能的一部分会实时发送到用户配置的上游；
  响应 SSE 按字节实时透传。仅在显式设置 `API_MOCK_OUTGOING_CAPTURE_DIR` 后，所有
  真实上游请求才会创建 capture：请求 `.json` 保留脱敏结构，`.response` 保留原始上游
  响应体（每条最多 8 MiB），配对的 `.profile.json` 记录安全响应头、文件名、截断状态和
  最终结果摘要（结果类别、HTTP 状态、流式完成标记、耗时）。响应 capture 可能包含模型
  输出、工具参数/结果或上游错误原文，必须仅保留在私有 capture 卷中，不能提交、公开、
  打包进镜像或发送到公开 issue。
- 普通 agent 调用可使用部署机的只读私有目录提供系统提示词和 WorkBuddy 请求头画像；
  `API_MOCK_PRIVATE_PROFILE_DIR` 指定宿主机目录，另外两个变量指定容器内读取路径。
  公开镜像不包含这些内容。仅允许 WorkBuddy 相关白名单头，账户池上游认证始终由服务端设置。
- 只有用户在 `.env` 明确设置 `API_MOCK_WORKBUDDY_USER_ID` 时，该标识才会加入
  上游请求头；留空时不会生成或转发用户标识。
