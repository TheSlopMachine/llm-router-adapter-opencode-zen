package opencodezen

import (
	"context"
	"fmt"
	"io"
	"strings"

	sdk "github.com/TheSlopMachine/llm-router-sdk"
)

const (
	adapterTypeKey = "opencode-zen"
	defaultBaseURL = "https://opencode.ai/zen/v1"
)

func init() {
	sdk.Register(&Adapter{})
}

type Adapter struct {
	client *Client
}

func (a *Adapter) TypeKey() string {
	return adapterTypeKey
}

func (a *Adapter) AuthType() sdk.AuthType {
	return sdk.AuthTypeAPIKey
}

func (a *Adapter) ValidateCredentials(data map[string]string) error {
	// nologin free API — no credentials required.
	// Allow empty (anonymous) or optional api_key for paid models.
	if len(data) == 0 {
		return nil
	}
	apiKey := strings.TrimSpace(data["api_key"])
	if apiKey == "" {
		apiKey = strings.TrimSpace(data["access_token"])
	}
	// if key provided, validate length; otherwise anonymous is ok
	if apiKey != "" && len(apiKey) < 20 {
		return fmt.Errorf("opencode-zen: api_key appears invalid (too short, min 20 chars)")
	}
	return nil
}

func (a *Adapter) Complete(
	ctx context.Context,
	cred *sdk.Credential,
	req *sdk.ChatCompletionRequest,
) (*sdk.ChatCompletionResponse, error) {
	apiKey := credentialAPIKey(cred)
	_, modelName, err := req.Model.Parse()
	if err != nil {
		return nil, fmt.Errorf("invalid model id: %w", err)
	}
	return a.getClient().ChatCompletion(ctx, apiKey, modelName, req)
}

func (a *Adapter) CompleteStream(
	ctx context.Context,
	cred *sdk.Credential,
	req *sdk.ChatCompletionRequest,
	w io.Writer,
) error {
	apiKey := credentialAPIKey(cred)
	_, modelName, err := req.Model.Parse()
	if err != nil {
		return fmt.Errorf("invalid model id: %w", err)
	}
	return a.getClient().ChatCompletionStream(ctx, apiKey, modelName, req, w)
}

func (a *Adapter) NeedsRefresh(cred *sdk.Credential) bool {
	return false
}

func (a *Adapter) RefreshCredential(
	ctx context.Context,
	cred *sdk.Credential,
) (*sdk.Credential, error) {
	return nil, sdk.ErrNoRefreshNeeded
}

func (a *Adapter) GetModelInfos(
	ctx context.Context,
	cred *sdk.Credential,
	providerQualifier string,
) ([]sdk.ModelInfo, error) {
	_ = providerQualifier
	apiKey := credentialAPIKey(cred)
	infos, err := a.getClient().ListModels(ctx, apiKey)
	if err == nil && len(infos) > 0 {
		return infos, nil
	}
	// fallback — free nologin models (allowAnonymous) + paid via key
	// Endpoints: /chat/completions (oa-compat), /responses (openai), /messages (anthropic)
	return []sdk.ModelInfo{
		// free nologin (oa-compat) — confirmed anonymous via handler:allowAnonymous
		{Name: "nemotron-3-ultra-free", DisplayName: "Nemotron 3 Ultra Free", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		{Name: "nemotron-3.5-lightning-free", DisplayName: "Nemotron 3.5 Lightning Free", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		{Name: "mimo-v2.5-free", DisplayName: "MiMo V2.5 Free", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		{Name: "big-pickle", DisplayName: "Big Pickle", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		{Name: "ling-3.0-flash-fin-free", DisplayName: "Ling 3.0 Flash Fin Free", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		// free contributor (openai /responses)
		{Name: "muse-spark-1.2-contributor-free", DisplayName: "Muse Spark 1.2 Contributor Free", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		{Name: "muse-spark-1.3-contributor-free", DisplayName: "Muse Spark 1.3 Contributor Free", ContextWindow: 128000, MaxTokens: 16384, RPM: 60, TPM: 100000, RPD: 500},
		// paid / with key examples (also available anonymous via IP rate limit but better with key)
		{Name: "gpt-5", DisplayName: "GPT-5", ContextWindow: 400000, MaxTokens: 32000, RPM: 1000, TPM: 1000000, RPD: 1500},
		{Name: "claude-sonnet-4.5", DisplayName: "Claude Sonnet 4.5", ContextWindow: 200000, MaxTokens: 32000, RPM: 500, TPM: 500000, RPD: 1000},
		{Name: "gemini-3-flash", DisplayName: "Gemini 3 Flash", ContextWindow: 1000000, MaxTokens: 32000, RPM: 1000, TPM: 1000000, RPD: 1500},
	}, nil
}

func (a *Adapter) GetAuthFlow() sdk.AuthFlowHandler {
	return &AuthFlow{}
}

func (a *Adapter) GetDefaultProviders() []sdk.ProviderInfo {
	return []sdk.ProviderInfo{
		{
			Name:      "OpenCode Zen",
			Qualifier: "",
			BaseURL:   defaultBaseURL,
			IconURL:   "https://opencode.ai/favicon.ico",
		},
	}
}

func (a *Adapter) getClient() *Client {
	if a.client == nil {
		a.client = newClient(defaultBaseURL)
	}
	return a.client
}

func credentialAPIKey(cred *sdk.Credential) string {
	if cred == nil {
		return ""
	}
	if v := strings.TrimSpace(cred.Data["api_key"]); v != "" {
		return v
	}
	return strings.TrimSpace(cred.Data["access_token"])
}

// AuthFlow — nologin free: no credentials required.
// Optional: user can supply API key for paid models, but anonymous works for free tier
// (handler:allowAnonymous + ipRateLimiter, see handler.ts:121,672).

type AuthFlow struct{}

func (f *AuthFlow) InitiateFlow(ctx sdk.AuthFlowContext) (sdk.AuthFlowState, error) {
	return sdk.AuthFlowState{
		RenderHTML: `
<div class="auth-flow-content">
	<p><strong>OpenCode Zen — Free (no login)</strong></p>
	<p>Free models work without API key (IP rate-limited, <code>allowAnonymous</code> in <code>model.ts:28</code>).</p>
	<p>Endpoints: <code>https://opencode.ai/zen/v1/chat/completions</code> (oa-compat), <code>/responses</code>, <code>/messages</code></p>
	<p>For paid models, paste your key from <a href="https://opencode.ai/zen" target="_blank">opencode.ai/zen</a>. Leave empty for free tier.</p>
	<div class="form-group">
		<label for="api_key">API Key (optional)</label>
		<input type="text" id="api_key" name="api_key" class="form-control" placeholder="sk-... (leave empty for free)" />
	</div>
	<button type="submit" class="btn btn-primary">Add Credential</button>
</div>`,
	}, nil
}

func (f *AuthFlow) HandleStep(ctx sdk.AuthFlowContext, input map[string][]string) (sdk.AuthFlowState, error) {
	vals, _ := input["api_key"]
	apiKey := ""
	if len(vals) > 0 {
		apiKey = strings.TrimSpace(vals[0])
	}
	if apiKey == "" {
		// nologin free
		return sdk.AuthFlowState{
			Credentials: map[string]string{},
		}, nil
	}
	if len(apiKey) < 20 {
		return sdk.AuthFlowState{
			RenderHTML: `
<div class="auth-flow-content">
	<div class="alert alert-danger">API key too short (min 20 chars) — leave empty for free tier</div>
	<p><strong>OpenCode Zen — Free (no login)</strong></p>
	<div class="form-group">
		<label for="api_key">API Key (optional)</label>
		<input type="text" id="api_key" name="api_key" class="form-control" placeholder="sk-... (leave empty for free)" />
	</div>
	<button type="submit" class="btn btn-primary">Add Credential</button>
</div>`,
		}, nil
	}
	return sdk.AuthFlowState{
		Credentials: map[string]string{"api_key": apiKey},
	}, nil
}

var _ sdk.Adapter = (*Adapter)(nil)
