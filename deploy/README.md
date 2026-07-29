# Buddy-API-mock Docker

Private local testing only. The service binds host loopback `127.0.0.1:13100`
via `network_mode: host`.

## End-user start (published image only)

```bash
cd deploy
cp .env.example .env
# set API_MOCK_ADMIN_PASSWORD and API_MOCK_API_KEY
docker compose -f docker-compose.yml pull
docker compose -f docker-compose.yml up -d
```

Open `http://127.0.0.1:13100` for the console (admin password).

Call the relay with the **relay** API key:

```bash
curl http://127.0.0.1:13100/v1/chat/completions \
  -H "Authorization: Bearer $API_MOCK_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt","messages":[{"role":"user","content":"hi"}]}'
```

Default image: `docker.io/ramudaderuta/buddy-api-mock:latest`.

## Notes

- `API_MOCK_ADMIN_PASSWORD`: Web console only
- `API_MOCK_API_KEY`: required for `/v1/chat/completions` clients
- `API_MOCK_WORKBUDDY_USER_ID`: optional WorkBuddy user ID forwarded to the upstream; keep it only in local `.env`
- Upstream provider keys are configured in the Account Pool UI, not in `.env`
- Named volume `api-mock-data` keeps encrypted accounts; `down` does not delete it
- WorkBuddy `conversation_topic` requests receive a fixed local SSE title; normal conversations still use the configured upstream account
- Keep administrator password, relay key, and the optional WorkBuddy user ID only in the ignored `deploy/.env`
- Real upstream endpoints and keys belong only in the local Account Pool; keys are encrypted in the named data volume and must not be added to source or Compose
- Normal conversation bodies must reach the user-configured upstream for relay operation, but prompts and upstream response bodies are never persisted
