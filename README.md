# CLIProxyAPI OpenCode Go Plugin

This ABI v1 plugin adds **OpenCode Go / Go Plus** (`opencode-go`) support to CLIProxyAPI, including:
- **OAuth Device Login**: Sign in directly from the CLIProxyAPI Management Console via `https://opencode.ai/console/auth/device/code` (or upload a JSON credential with `api_key` / `access_token`). Automatically refreshes tokens and attaches `x-opencode-org-id` and `x-opencode-session` (`cpa-main` by default).
- **Full 44-Model Multi-Protocol Catalog**: Dynamically merges live `/inference/go/v1/models` and `models.opencode.ai/api.json` metadata and routes each model to its native upstream wire protocol (`sdk/translator/builtin` handles cross-protocol client translation):
  - **OpenAI Chat Completions** (`/inference/go/openai/v1/chat/completions`): `glm-5.3`, `glm-5.3-flash`, `glm-5.2`, `glm-5.1`, `kimi-k3.1-pro`, `kimi-k3`, `kimi-k2.7-code`, `kimi-k2.6`, `deepseek-v4.1-flash`, `deepseek-v4-pro`, `mimo-v2.6-pro`, `mimo-v2.6-flash`, `mimo-v2.5-pro`, `mimo-v2.5`, `minimax-m3.1-flash`, `minimax-m2.5`, `longcat-2.0`, `hy4-preview`, `hy3`, `step-5-preview`, `omen-alpha`, `beam-free`, `cat-jam-free`, `iceman-contributor-free`, `longcat-2.5-preview-free`, `minimax-m3.1-flash-free`, `space-bunny`, `step-5-preview-free`
  - **OpenAI Responses** (`/inference/go/openai/v1/responses`): `gpt-6-luna`, `gpt-5.6-luna`, `grok-4.7`, `grok-4.6`, `muse-spark-1.3-contributor`, `muse-spark-1.2-contributor`
  - **Anthropic Messages** (`/inference/go/anthropic/v1/messages`): `claude-haiku-5-5`, `minimax-m3`, `minimax-m2.7`, `qwen3.8-max`, `qwen3.8-flash`, `qwen3.7-plus`, `qwen3.7-max`, `qwen3.6-plus`
  - **Google Gemini** (`/inference/go/google/v1beta/models/<model>:generateContent`): `gemini-4-argon`, `gemini-4-argon-contributor`
- **Quota & Per-Model Allowance Telemetry**: Extracts `usage.opencode` (`billing_source`, `list_cost`, `allowance`, token breakdown) from every response, exposes 5-hour / weekly / monthly rolling buckets via `QuotaProvider`.

## Build

```bash
make test
```

Produces `build/opencode-go.so`.

## Install from the management interface

Add this registry URL to `plugins.store-sources`:

```yaml
plugins:
  enabled: true
  store-sources:
    - "https://raw.githubusercontent.com/esauvisky/CLIProxyAPI-OpenCode-Go-Plugin/main/registry.json"
```

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    opencode-go:
      enabled: true
      priority: 10
      session_id: "cpa-main"
      plan: "auto" # "auto", "go", or "go-plus"
```
