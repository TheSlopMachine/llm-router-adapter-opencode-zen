package opencodezen

import (
	sdk "github.com/TheSlopMachine/llm-router-sdk"
)

// transformRequest — passthrough; upstream is OpenAI-compatible.
// We only ensure model field matches the short model name (without provider prefix).

func transformRequest(req *sdk.ChatCompletionRequest, modelName string) *sdk.ChatCompletionRequest {
	// shallow copy with normalized model
	out := *req
	out.Model = sdk.ModelId(modelName)
	// upstream expects stream flag based on endpoint, caller sets it
	return &out
}
