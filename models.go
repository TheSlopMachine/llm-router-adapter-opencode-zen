package opencodezen

// OpenCode Zen is OpenAI-compatible; we reuse OpenAI wire types
// but keep minimal upstream structs for /models and error parsing.

type upstreamModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type upstreamModelsResponse struct {
	Object string          `json:"object"`
	Data   []upstreamModel `json:"data"`
}

type upstreamErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    interface{} `json:"code"`
	} `json:"error"`
}
