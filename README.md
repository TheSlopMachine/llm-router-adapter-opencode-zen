# OpenCode Zen Adapter for llm-router

Adapter `opencode-zen` for `llm-router` (`ModelId = opencode-zen/<model>`). **Nologin free** — реверс `opencode` Zen (`packages/console/app/src/routes/zen/util/handler.ts:121,672`, `packages/console/core/src/model.ts:28` `allowAnonymous`).

## How Free API Works (reverse-engineered from `sst/opencode`)

- Base: `https://opencode.ai/zen/v1`
- Endpoints: `POST /chat/completions` (`oa-compat`), `POST /responses` (`openai`), `POST /messages` (`anthropic`), `GET /models`
- Free models have `allowAnonymous:true` — `handler.ts:102,121` uses `zenApiKey = undefined` when header is missing or `public`, `rateLimiter = ipRateLimiter` + `authenticate` skipped, billing `"anonymous"`/`"free"` not charged (`cost:"0"`).
- Rate limit: IP-based via `ipRateLimiter.ts` (Redis `buildRateLimitKey("ip", ip)`), `FreeUsageLimitError` on exceed. Paid models require `Authorization: Bearer <key>`.

Cloned discovery: `C:/tmp/opencode` (`git clone https://github.com/sst/opencode.git`) — see `packages/console/app/src/routes/zen/v1/chat/completions.ts:9`, `handler.ts:121-124`, `model.ts:28`.

## Installation

Add to `adapters.conf`:

```
github.com/TheSlopMachine/llm-router-adapter-opencode-zen
```

Then:

```bash
make prepare-workspace   # generates adapters.go + go.work
make go-check
make start
```

## Credential Shape — NOLOGIN

```json
{}
```

Empty credentials accepted (`ValidateCredentials` allows empty). Optional for paid models:

```json
{
  "api_key": "sk-..."
}
```

Stored as `sdk.Credential.Data["api_key"]`. Dashboard auth flow: "Leave empty for free tier".

## Supported Models

Fetched via `GET https://opencode.ai/zen/v1/models` (no auth needed, 66 models). Fallback free catalog (IP rate-limited):

- `opencode-zen/nemotron-3-ultra-free` (oa-compat `chat/completions`) — confirmed `pong` w/out key
- `opencode-zen/nemotron-3.5-lightning-free`
- `opencode-zen/mimo-v2.5-free`, `big-pickle`, `ling-3.0-flash-fin-free`
- `opencode-zen/muse-spark-1.2-contributor-free` (`/responses`)
- `opencode-zen/muse-spark-1.3-contributor-free`

Plus paid with key: `gpt-5`, `claude-sonnet-4.5`, `gemini-3-flash`, etc.

## Features

- Non-streaming `POST /v1/chat/completions` + streaming SSE `data: [DONE]` (passthrough)
- Auto routing by model: `gpt-*/muse-spark*/grok*` → `/responses`, `claude-*/qwen*` → `/messages`, others → `/chat/completions`
- `GetModelInfos` + hardcoded fallback
- No-login auth flow

## Example Usage

```bash
# free nologin (empty credential)
curl -H "Authorization: Bearer $key" http://localhost:8081/v1/models
curl -H "Authorization: Bearer $key" -H "Content-Type: application/json" \
  -d '{"model":"opencode-zen/nemotron-3-ultra-free","messages":[{"role":"user","content":"ping"}]}' \
  http://localhost:8081/v1/chat/completions

# direct upstream check (no key)
curl -X POST https://opencode.ai/zen/v1/chat/completions -H "Content-Type: application/json" \
  -d '{"model":"nemotron-3-ultra-free","messages":[{"role":"user","content":"ping"}],"stream":false}'
curl -X POST https://opencode.ai/zen/v1/responses -H "Content-Type: application/json" \
  -d '{"model":"muse-spark-1.2-contributor-free","input":"ping","stream":false}'
```

## License

MIT
