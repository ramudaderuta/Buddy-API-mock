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

客户端调用中转服务时使用**中转 API Key**。先查询已启用账户提供的模型 ID：

```bash
curl http://127.0.0.1:13100/v1/models \
  -H "Authorization: Bearer $API_MOCK_API_KEY"
```

从响应的 `data` 数组中选择一个对象的 `id`，填入下方的 `<model-id>`，再调用 Chat Completions：

```bash
curl http://127.0.0.1:13100/v1/chat/completions \
  -H "Authorization: Bearer $API_MOCK_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"<model-id>","messages":[{"role":"user","content":"hi"}]}'
```

默认镜像：`docker.io/ramudaderuta/buddy-api-mock:latest`。

## 本次更新后的迁移注意事项

本次更新将 `/data` 和 `/outgoing-captures` 的可写权限内置到镜像，服务主进程仍以 distroless `nonroot` 用户（UID/GID `65532`）运行。同时，outgoing captures 从宿主机目录 bind mount 改为命名卷 `api-mock-captures`。

### 1. 升级前先备份，不要删除卷

```bash
cd deploy
docker compose -f docker-compose.yml down

# 查看实际卷名；Compose 通常会加上项目名前缀 buddy-api-mock_。
docker volume ls | grep api-mock

# 将下面的卷名替换为上一步显示的实际名称。
docker run --rm \
  -v buddy-api-mock_api-mock-data:/source:ro \
  -v "$PWD/.private/backup:/backup" \
  alpine:3.20 \
  sh -c 'cd /source && tar czf /backup/api-mock-data-before-nonroot-migration.tar.gz .'
```

升级期间不要执行 `docker compose down -v`，也不要手动删除 `api-mock-data`；否则账户池、加密密钥和请求记录会丢失。

### 2. 已有 `api-mock-data` 卷的权限迁移

新建命名卷会自动继承镜像中 UID/GID `65532` 的目录权限，无需额外处理。旧版已经创建过的卷不会重新继承镜像权限；如果升级后日志出现 `/data` 的 `permission denied`，在服务停止状态下执行一次：

```bash
# 将卷名替换为 docker volume ls 显示的实际名称。
docker run --rm --user 0:0 \
  -v buddy-api-mock_api-mock-data:/data \
  alpine:3.20 \
  sh -c 'chown -R 65532:65532 /data && chmod 700 /data'
```

该命令只调整命名卷内部的所有者和目录权限，不会让 Buddy-API-mock 主进程以 root 运行。

### 3. 旧 captures 目录迁移到命名卷

旧版 Compose 使用 `API_MOCK_OUTGOING_CAPTURE_HOST_DIR` 指定宿主机目录；新版不再读取这个变量，而是挂载命名卷 `api-mock-captures`。如果不需要保留历史捕获，可直接启动，新卷会自动创建。

需要保留历史捕获时，先创建新卷并复制文件：

```bash
cd deploy
docker volume create buddy-api-mock_api-mock-captures

# 将 ./旧捕获目录 替换为原 API_MOCK_OUTGOING_CAPTURE_HOST_DIR 的值；
# 将卷名替换为 docker volume ls 显示的实际名称。
docker run --rm --user 0:0 \
  -v "$PWD/旧捕获目录:/source:ro" \
  -v buddy-api-mock_api-mock-captures:/destination \
  alpine:3.20 \
  sh -c 'cp -a /source/. /destination/ && chown -R 65532:65532 /destination && chmod 700 /destination'
```

历史捕获由旧版本产生，可能包含完整请求正文和用户内容，应继续按私有数据处理，不要提交到 Git、上传到公开 issue 或打包进镜像。新版 capture 会保留请求结构，但把消息正文、typed text、工具参数、URL、凭证类字段及常见用户/会话/请求/链路标识替换为 `[REDACTED]`；这只是轻量规则脱敏，命名卷仍应视为私有数据。

### 4. Docker 代理与源码构建

`docker pull`、`docker login` 和基础镜像解析由 Docker daemon 发起，daemon 不会继承当前交互式 shell 后来设置的代理变量；需要通过 systemd drop-in 或 Docker daemon 配置设置代理。维护者从源码构建时，Dockerfile 的 `RUN` 步骤还需要访问代理；`compose.yaml` 已使用 `build.network: host` 并把 shell 中的 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` 作为 Docker 预定义 build args 临时传入。

这些 build args 不会写入最终镜像；不要在 Dockerfile 中使用 `ENV HTTP_PROXY=...`，也不要把带凭据的代理 URL 写入 Compose 或提交到 Git。仅拉取发布镜像的 `docker-compose.yml` 不需要 build 配置。

### 5. 更新并验证

```bash
cd deploy
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
docker compose -f docker-compose.yml ps
docker compose -f docker-compose.yml logs --tail 100
curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:13100/
```

预期容器状态为 `Up`，HTTP 状态为 `200`，日志中不应出现 `/data` 或 `/outgoing-captures` 的 `permission denied`。

### 6. 回滚

普通回滚只需停止容器并将镜像切回旧版本；不要删除命名卷。若必须恢复数据，可停止服务后将升级前的 tar 备份解压回 `api-mock-data`，随后再次将卷内文件所有者设为目标镜像所需的 UID/GID。

## 说明

- `API_MOCK_ADMIN_PASSWORD`：仅用于 Web 控制台管理员登录。
- `API_MOCK_API_KEY`：客户端调用 `/v1/chat/completions` 时必须提供的中转 Key。
- `API_MOCK_WORKBUDDY_USER_ID`：可选；仅在显式设置时转发给上游。请只保留在本机 `.env`。
- `API_MOCK_PRIVATE_PROFILE_DIR`：可选；宿主机私有画像目录，以只读方式挂载到容器 `/private-profile`。默认 `./.private/workbuddy-profile`。
- `API_MOCK_MODEL_INSTRUCTIONS_FILE`：可选；容器内私有系统提示词路径，例如 `/private-profile/model_instructions.private.md`。
- `API_MOCK_WORKBUDDY_PROFILE_FILE`：可选；容器内私有 WorkBuddy 请求头画像路径，例如 `/private-profile/workbuddy_profile.private.json`。
- 上游 Endpoint 和 API Key 在控制台「账户池」配置；上游 Key 不写入 `.env`。
- 命名卷 `api-mock-data` 保存加密账户数据，`api-mock-captures` 保存可选 capture。启用 `API_MOCK_OUTGOING_CAPTURE_DIR` 后，每个真实上游请求会保存脱敏请求结构、受白名单限制的响应头和最终结果元数据；同名 `.response` 文件保存原始上游响应体，每条最多 8 MiB，并以 profile 的 `truncated` 标记说明是否截断。响应文件可能含模型输出、工具参数/结果或错误原文，必须按私有数据处理，绝不能提交、公开、发送到公开 issue 或打包进镜像。镜像为两个挂载点预置 UID/GID 65532 权限，因此主进程可始终以 nonroot 运行。普通 `docker compose down` 不会删除这些卷。
- WorkBuddy 的 `conversation_topic` 请求由本地返回固定 SSE 标题；正式对话仍转发至已配置上游。
- 弱网参数全部固定在程序中，不新增环境变量：连接/TLS/响应头分段超时、非流式总时限、SSE flush/heartbeat/空闲保护，以及仅限请求尚未写出阶段的安全重试。
- 一个逻辑请求最多执行 10 次安全 pre-write 尝试，前 9 次失败后按 `250ms、500ms、1s、2s、4s、8s、16s、32s、60s` 退避；第 10 次仍失败时立即返回。该序列只使用当前选定账户，不切换账户、不循环，也不维护账户冷却或暂停调度状态。
- 自动重试不会跨越 HTTP 响应或任何已写出的请求/响应字节；HTTP 4xx/5xx、SSE error、SSE 缺少 `[DONE]`、流中断和工具调用都不会透明重放，由客户端决定是否提交新的逻辑请求。
- Cloudflare 路径应继续禁用 `/v1/chat/completions` 缓存和响应转换；应用会为 SSE 设置 `no-cache, no-transform` 与 `X-Accel-Buffering: no` 并发送标准注释 heartbeat。
- 管理员密码、中转 Key、可选用户标识只放在被忽略的 `deploy/.env`。
- 真实上游 Endpoint 和 Key 只保存在本机账户池及加密数据卷，不能写入源码或 Compose。
- 正式对话正文为实现中转所必需会发送给用户配置的上游。仅当显式设置 `API_MOCK_OUTGOING_CAPTURE_DIR` 时，服务才会将脱敏请求结构、受限响应元数据和原始响应体写入私有 capture 卷；响应体固定上限为每条 8 MiB。
