# OpenCode Zen Adapter for llm-router

Adapter `opencode-zen` for `llm-router` (`ModelId = opencode-zen/<model>`).

Lightweight gateway to `https://opencode.ai/zen/v1` with automatic free-tier handling.

## Installation

Add to `adapters.conf`:

```
github.com/TheSlopMachine/llm-router-adapter-opencode-zen
```

Then:

```bash
make prepare-workspace
make go-check
make start
```

## Credentials

No credentials required for free-tier models. The adapter works out of the box.

For higher limits or paid models, add an optional API key from `https://opencode.ai/zen`:

```json
{
  "api_key": "sk-..."
}
```

Leave the field empty in the dashboard to use the free tier.

## Supported Models

Models are discovered via `GET https://opencode.ai/zen/v1/models` (66 models). Built-in fallback includes:

- `opencode-zen/nemotron-3-ultra-free`
- `opencode-zen/nemotron-3.5-lightning-free`
- `opencode-zen/mimo-v2.5-free`, `big-pickle`, `ling-3.0-flash-fin-free`
- `opencode-zen/muse-spark-1.2-contributor-free`
- `opencode-zen/muse-spark-1.3-contributor-free`

Plus any model available on Zen with an API key (e.g. `gpt-5`, `claude-sonnet-4.5`, `gemini-3-flash`).

## Features

- `POST /v1/chat/completions` (non-streaming + SSE `data: [DONE]`)
- Automatic routing by model family to the appropriate Zen endpoint
- Streaming tool calls (including parallel) with correct `index`/`finish_reason`
- No-login auth flow (IP-based rate limiting on free tier)

## Example

```bash
curl -H "Authorization: Bearer $key" http://localhost:8081/v1/models

curl -H "Authorization: Bearer $key" -H "Content-Type: application/json" \
  -d '{"model":"opencode-zen/nemotron-3-ultra-free","messages":[{"role":"user","content":"ping"}]}' \
  http://localhost:8081/v1/chat/completions
```

## License

MIT
