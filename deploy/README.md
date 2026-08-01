# Buddy-API-mock Docker 部署

仅用于本机/个人使用。服务通过 `network_mode: host` 绑定主机回环地址
`127.0.0.1:13100`。

## 使用者启动（仅拉取已发布镜像）

```bash
cd deploy
cp .env.example .env
# 设置 API_MOCK_ADMIN_PASSWORD 与 API_MOCK_API_KEY
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
```

浏览器打开 `http://127.0.0.1:13100`，使用管理员密码登录控制台。

客户端调用中转服务时使用**中转 API Key**。先读取已启用账户提供的模型 ID：

```bash
curl http://127.0.0.1:13100/v1/models \
  -H "Authorization: Bearer $API_MOCK_API_KEY"
```

再使用返回的模型 ID 调用 Chat Completions：

```bash
curl http://127.0.0.1:13100/v1/chat/completions \
  -H "Authorization: Bearer $API_MOCK_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"<model-id>","messages":[{"role":"user","content":"hi"}]}'
```

默认镜像：`docker.io/ramudaderuta/buddy-api-mock:latest`。

## 说明

- `API_MOCK_ADMIN_PASSWORD`：仅用于 Web 控制台管理员登录。
- `API_MOCK_API_KEY`：客户端调用 `/v1/chat/completions` 时必须提供的中转 Key。
- `API_MOCK_WORKBUDDY_USER_ID`：可选；仅在显式设置时转发给上游。请只保留在本机 `.env`。
- `API_MOCK_PRIVATE_PROFILE_DIR`：可选；宿主机私有画像目录，以只读方式挂载到容器 `/private-profile`。默认 `./.private/workbuddy-profile`。
- `API_MOCK_MODEL_INSTRUCTIONS_FILE`：可选；容器内私有系统提示词路径，例如 `/private-profile/model_instructions.private.md`。
- `API_MOCK_WORKBUDDY_PROFILE_FILE`：可选；容器内私有 WorkBuddy 请求头画像路径，例如 `/private-profile/workbuddy_profile.private.json`。
- 上游 Endpoint 和 API Key 在控制台「账户池」配置；上游 Key 不写入 `.env`。
- 命名卷 `api-mock-data` 保存加密账户数据；普通 `docker compose down` 不会删除它。
- WorkBuddy 的 `conversation_topic` 请求由本地返回固定 SSE 标题；正式对话仍转发至已配置上游。
- 管理员密码、中转 Key、可选用户标识只放在被忽略的 `deploy/.env`。
- 真实上游 Endpoint 和 Key 只保存在本机账户池及加密数据卷，不能写入源码或 Compose。
- 正式对话正文为实现中转所必需会发送给用户配置的上游；服务不会持久化提示词或上游响应正文。
