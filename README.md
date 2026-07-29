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
- 内置 WorkBuddy 兼容 `Skill` 描述（`prompts/skill_description.txt`，编译进二进制/镜像），并强制 `tool_choice: "none"`
- WorkBuddy 的 `conversation_topic` 阶段在本地返回固定标题 SSE，不向上游发送该阶段的提示词；正式 `conversation` 仍使用账户池转发

## 本机直接运行

```bash
API_MOCK_ADMIN_PASSWORD='local-password' \
API_MOCK_API_KEY='local-relay-key' \
go run ./cmd/api-mock
```

默认使用源码内嵌的 Skill 描述。如需覆盖：

```bash
API_MOCK_ADMIN_PASSWORD='local-password' \
API_MOCK_SKILL_DESCRIPTION_FILE='/path/to/custom-skill-description.txt' \
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
| `API_MOCK_IMAGE` | 可选。默认 `docker.io/ramudaderuta/buddy-api-mock:latest`。 |
| `API_MOCK_SKILL_DESCRIPTION_FILE` | 可选。覆盖镜像内嵌的 Skill 描述；默认不需要设置。 |

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

### 3. 常用运维

```bash
cd deploy
docker compose -f docker-compose.yml logs -f --tail 100
docker compose -f docker-compose.yml restart
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml down
```

### 4. 从本仓库源码构建（维护者）

```bash
cd deploy
docker compose -f compose.yaml up -d --build
```

### 5. 卸载

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
- **Skill 描述**：默认编译进镜像；不进 data 卷。请求记录仍不保存提示词/密钥/上游正文。

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
- 只有用户在 `.env` 明确设置 `API_MOCK_WORKBUDDY_USER_ID` 时，该标识才会加入
  上游请求头；留空时不会生成或转发用户标识。
