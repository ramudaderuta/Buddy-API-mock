# Buddy-API-mock

`Buddy-API-mock`（仓库名 `api-mock`）是一个仅供本机/个人使用的 OpenAI Chat
Completions 中转控制台。它通过本地账户池转发请求，只持久化加密后的账户密钥与
请求元数据，不保存提示词、明文密钥或上游响应正文。

控制台默认监听 `127.0.0.1:13100`。不要把该端口直接暴露到公网。

## 功能概览

- 标准 `POST /v1/chat/completions` 中转（JSON / SSE）
- 管理员密码登录的本机控制台：账户池、请求记录
- 调度策略：默认 **填充优先**，可切换 **轮询优先**
- 账户 Endpoint 必须为 HTTPS；本地 AES-GCM 加密保存 API Key
- 内置固定系统提示词；原生 WorkBuddy 正式请求保留其消息画像并移除内置工具声明
- WorkBuddy 的 `conversation_topic` 阶段在本地返回固定标题 SSE，不向上游发送该阶段的提示词；正式 `conversation` 仍使用账户池转发

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

发送标准 Chat Completions 请求。

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
| `API_MOCK_MODEL_INSTRUCTIONS_FILE` | 可选。容器内私有系统提示词文件。普通 agent 请求使用它构造 WorkBuddy 画像；文件只放在部署机数据卷，不能提交或复制进镜像。 |
| `API_MOCK_WORKBUDDY_PROFILE_FILE` | 可选。容器内私有 WorkBuddy 请求头画像。普通 agent 请求使用其中白名单头；文件只放在部署机数据卷，不能提交或复制进镜像。 |
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

将这两个文件放在一个仅当前用户可读的本地目录，例如 `./.private/workbuddy-profile`，然后写入 Docker
数据卷。下面的命令只复制文件，不会输出它们的内容：

```bash
export PRIVATE_DIR=./.private/workbuddy-profile

docker run --rm \
  -v "$PRIVATE_DIR:/input:ro" \
  -v buddy-api-mock_api-mock-data:/data \
  alpine:3.20 \
  sh -c '
    test -s /input/model_instructions.private.md &&
    test -s /input/workbuddy_profile.private.json &&
    cp /input/model_instructions.private.md /data/model_instructions.private.md &&
    cp /input/workbuddy_profile.private.json /data/workbuddy_profile.private.json &&
    chown 65532:65532 /data/model_instructions.private.md /data/workbuddy_profile.private.json &&
    chmod 600 /data/model_instructions.private.md /data/workbuddy_profile.private.json
  '
```

在 `deploy/.env` 设置：

```dotenv
API_MOCK_MODEL_INSTRUCTIONS_FILE=/data/model_instructions.private.md
API_MOCK_WORKBUDDY_PROFILE_FILE=/data/workbuddy_profile.private.json
```

然后重建容器以加载新的 Compose 环境变量：

```bash
cd deploy
docker compose -f docker-compose.yml up -d
```

普通 Agent 请求的 `tools` 和 `tool_choice` 会原样转发；服务不再注入 WorkBuddy 工具。
仅验证文件存在且运行账户可读时，可执行：

```bash
docker run --rm --user 65532:65532 \
  -v buddy-api-mock_api-mock-data:/data \
  alpine:3.20 \
  sh -c 'test -r /data/model_instructions.private.md && test -r /data/workbuddy_profile.private.json'
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

客户端必须携带本服务的 `API_MOCK_API_KEY`（与控制台管理员密码、上游账户 Key 都不同）：

```bash
curl http://127.0.0.1:13100/v1/chat/completions \
  -H "Authorization: Bearer $API_MOCK_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt","messages":[{"role":"user","content":"hi"}]}'
```

也支持 `X-API-Key: <API_MOCK_API_KEY>`。缺少或错误时返回 `401 invalid api key`。

上游真实密钥在控制台「账户池」中配置；转发时由服务自动替换请求头。

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
  响应 SSE 按字节实时透传。服务只持久化请求元数据，不保存提示词、明文密钥或
  上游响应正文。
- 普通 agent 调用需要在部署机数据卷内提供经过私有验证的系统提示词文件，并将
  `API_MOCK_MODEL_INSTRUCTIONS_FILE` 指向该文件；公开镜像不包含该内容。
- 普通 agent 调用还可配置 `API_MOCK_WORKBUDDY_PROFILE_FILE` 使用经私有验证的
  WorkBuddy 请求头画像。仅允许 WorkBuddy 相关白名单头，账户池上游认证始终由服务端设置。
- 只有用户在 `.env` 明确设置 `API_MOCK_WORKBUDDY_USER_ID` 时，该标识才会加入
  上游请求头；留空时不会生成或转发用户标识。
