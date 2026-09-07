package opencodezen

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/TheSlopMachine/llm-router-sdk"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func newClient(baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// endpointForModel returns upstream path + handling type.
// Based on zen.mdx: /chat/completions (oa-compat), /responses (openai), /messages (anthropic), /models/gemini (google).
// For nologin free we support oa-compat and openai directly; anthropic/google go via same passthrough if needed.
func endpointForModel(modelName string) string {
	lower := strings.ToLower(modelName)
	// openai /responses: gpt-*, muse-spark, grok
	if strings.HasPrefix(lower, "gpt-") || strings.Contains(lower, "muse-spark") || strings.HasPrefix(lower, "grok-") {
		return "/responses"
	}
	// anthropic /messages: claude-*, qwen3*
	if strings.HasPrefix(lower, "claude-") || strings.HasPrefix(lower, "qwen") {
		return "/messages"
	}
	// google: gemini-*
	if strings.HasPrefix(lower, "gemini-") {
		// google format uses /models/<model>:streamGenerateContent, but Zen exposes via /chat/completions-like? 
		// For simplicity route gemini via /chat/completions (Zen handles translation inside handler)
		// Fallback to chat/completions if specific google endpoint not known.
		return "/chat/completions"
	}
	// default oa-compat: deepseek, glm, kimi, minimax, nemotron, mimo, ling, big-pickle etc
	return "/chat/completions"
}

func (c *Client) ChatCompletion(
	ctx context.Context,
	apiKey, modelName string,
	req *sdk.ChatCompletionRequest,
) (*sdk.ChatCompletionResponse, error) {
	endpoint := endpointForModel(modelName)

	// For /responses (openai) we need to translate chat -> responses input
	if endpoint == "/responses" {
		return c.chatToResponses(ctx, apiKey, modelName, req, false, nil)
	}

	upstreamReq := transformRequest(req, modelName)
	upstreamReq.Stream = false

	body, err := json.Marshal(upstreamReq)
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	addOpenCodeHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseUpstreamError(resp.StatusCode, resp.Header, respBody)
	}

	// Handle anthropic messages wrapped response? For now assume oa-compat returns openai shape
	var out sdk.ChatCompletionResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("opencode-zen: parse response: %w", err)
	}
	if out.Model == "" {
		out.Model = string(req.Model)
	}
	return &out, nil
}

func (c *Client) ChatCompletionStream(
	ctx context.Context,
	apiKey, modelName string,
	req *sdk.ChatCompletionRequest,
	w io.Writer,
) error {
	endpoint := endpointForModel(modelName)
	if endpoint == "/responses" {
		_, err := c.chatToResponses(ctx, apiKey, modelName, req, true, w)
		return err
	}

	// For /chat/completions (oa-compat) free models, upstream streaming is unreliable (hangs with keep-alive).
	// Use non-stream call and synthesize SSE chunks to ensure tool_calls (including parallel) are correctly streamed.
	// This avoids Zed error "data did not match any variant of untagged enum ResponseStreamResult" caused by incomplete upstream SSE.
	resp, err := c.ChatCompletion(ctx, apiKey, modelName, req)
	if err != nil {
		return err
	}
	// Synthesize streaming chunks from complete response
	choice := resp.Choices[0]
	// First chunk: role
	chunk := sdk.StreamChunk{
		ID: resp.ID, Object: "chat.completion.chunk", Created: resp.Created, Model: resp.Model,
		Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{Role: "assistant"}, FinishReason: nil}},
	}
	b, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", b)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	// Content chunks (split by words for streaming effect)
	if text := strings.TrimSpace(choice.Message.Content); text != "" {
		// single chunk for simplicity
		chunk = sdk.StreamChunk{
			ID: resp.ID, Object: "chat.completion.chunk", Created: resp.Created, Model: resp.Model,
			Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{Content: text}}},
		}
		b, _ = json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	// Tool calls chunks — emit OpenAI-compatible chunks with index field (manual JSON because sdk.ChatToolCall has no Index)
	for idx, tc := range choice.Message.ToolCalls {
		// First chunk with id+name+index
		raw := map[string]any{
			"id": resp.ID, "object": "chat.completion.chunk", "created": resp.Created, "model": resp.Model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": idx, "id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Function.Name, "arguments": ""},
					}},
				},
				"finish_reason": nil,
			}},
		}
		b, _ := json.Marshal(raw)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		// Second chunk with arguments and same index
		raw = map[string]any{
			"id": resp.ID, "object": "chat.completion.chunk", "created": resp.Created, "model": resp.Model,
			"choices": []any{map[string]any{
				"index": 0,
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": idx, "function": map[string]any{"arguments": tc.Function.Arguments},
					}},
				},
				"finish_reason": nil,
			}},
		}
		b, _ = json.Marshal(raw)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	// Final chunk with finish_reason
	finish := choice.FinishReason
	if finish == "" {
		finish = "stop"
		if len(choice.Message.ToolCalls) > 0 {
			finish = "tool_calls"
		}
	}
	chunk = sdk.StreamChunk{
		ID: resp.ID, Object: "chat.completion.chunk", Created: resp.Created, Model: resp.Model,
		Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{}, FinishReason: &finish}},
	}
	b, _ = json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", b)
	fmt.Fprintln(w, "data: [DONE]")
	fmt.Fprintln(w, "")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// chatToResponses translates OpenAI chat/completions -> Zen /responses (openai format)
// Supports tools: maps sdk.ChatTool -> responses tools, and messages with tool_calls/tool results.
// See openai.ts:toOpenaiRequest/fromOpenaiResponse for reference.
func (c *Client) chatToResponses(
	ctx context.Context,
	apiKey, modelName string,
	req *sdk.ChatCompletionRequest,
	stream bool,
	w io.Writer,
) (*sdk.ChatCompletionResponse, error) {
	input := buildResponsesInput(req.Messages)
	if len(input) == 0 {
		input = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "ping"}}}}
	}
	payload := map[string]any{
		"model": modelName,
		"input": input,
		"stream": stream,
	}
	if req.MaxTokens > 0 {
		payload["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature > 0 {
		payload["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		payload["top_p"] = req.TopP
	}
	if tools := buildResponsesTools(req.Tools); len(tools) > 0 {
		payload["tools"] = tools
	}
	if tc := buildResponsesToolChoice(req.ToolChoice); tc != nil {
		payload["tool_choice"] = tc
	}

	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: create responses request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	addOpenCodeHeaders(httpReq)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: responses request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, parseUpstreamError(resp.StatusCode, resp.Header, b)
	}

	if !stream {
		b, _ := io.ReadAll(resp.Body)
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("opencode-zen: parse responses: %w", err)
		}
		text := extractResponsesText(raw)
		toolCalls := extractResponsesToolCalls(raw)
		finish := "stop"
		if len(toolCalls) > 0 {
			finish = "tool_calls"
		}
		return &sdk.ChatCompletionResponse{
			ID:      fmt.Sprintf("zen-%d", time.Now().UnixNano()),
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   string(req.Model),
			Choices: []sdk.ChatCompletionChoice{
				{Index: 0, Message: sdk.ChatMessage{Role: "assistant", Content: text, ToolCalls: toolCalls}, FinishReason: finish},
			},
			Usage: sdk.ChatCompletionUsage{
				PromptTokens:     int(extractUsage(raw, "input_tokens")),
				CompletionTokens: int(extractUsage(raw, "output_tokens")),
				TotalTokens:      int(extractUsage(raw, "total_tokens")),
			},
		}, nil
	}

	// stream: Zen emits responses SSE (event: / data: ), convert to chat.completion.chunk including tool_calls
	// Track tool call indices for parallel calls (output_index -> index)
	nextToolIdx := 0
	outputIdxToToolIdx := map[int]int{}
	callIdToToolIdx := map[string]int{}
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "event: ") {
			if !scanner.Scan() {
				break
			}
			dataLine := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}
			event := strings.TrimPrefix(trim, "event: ")
			data := strings.TrimPrefix(dataLine, "data: ")
			if data == "[DONE]" {
				fmt.Fprintln(w, "data: [DONE]")
				fmt.Fprintln(w, "")
				break
			}
			// parse payload for output_index tracking
			var payload map[string]any
			_ = json.Unmarshal([]byte(data), &payload)
			outputIdx := -1
			if v, ok := payload["output_index"].(float64); ok {
				outputIdx = int(v)
			}
			var rawChunk map[string]any
			switch event {
			case "response.output_text.delta":
				delta, _ := payload["delta"].(string)
				if delta == "" {
					delta, _ = payload["text"].(string)
				}
				if delta == "" {
					continue
				}
				rawChunk = map[string]any{
					"id": fmt.Sprintf("zen-%d", time.Now().UnixNano()), "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": string(req.Model),
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
				}
			case "response.output_item.added":
				if item, ok := payload["item"].(map[string]any); ok && item["type"] == "function_call" {
					name, _ := item["name"].(string)
					id, _ := item["id"].(string)
					if id == "" {
						id, _ = item["call_id"].(string)
					}
					if name != "" {
						idx := nextToolIdx
						nextToolIdx++
						if outputIdx >= 0 {
							outputIdxToToolIdx[outputIdx] = idx
						}
						if id != "" {
							callIdToToolIdx[id] = idx
						}
						rawChunk = map[string]any{
							"id": fmt.Sprintf("zen-%d", time.Now().UnixNano()), "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": string(req.Model),
							"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": idx, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": ""}}}}, "finish_reason": nil}},
						}
					}
				}
			case "response.function_call_arguments.delta":
				delta, _ := payload["delta"].(string)
				if delta == "" {
					delta, _ = payload["arguments_delta"].(string)
				}
				if delta != "" {
					idx := 0
					if outputIdx >= 0 {
						if mapped, ok := outputIdxToToolIdx[outputIdx]; ok {
							idx = mapped
						}
					}
					rawChunk = map[string]any{
						"id": fmt.Sprintf("zen-%d", time.Now().UnixNano()), "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": string(req.Model),
						"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": idx, "function": map[string]any{"arguments": delta}}}}, "finish_reason": nil}},
					}
				}
			case "response.completed":
				finish := "stop"
				// If we saw any tool calls in this stream, finish must be tool_calls (Zed expects it to trigger execution)
				if len(outputIdxToToolIdx) > 0 || len(callIdToToolIdx) > 0 {
					finish = "tool_calls"
				} else if respObj, ok := payload["response"].(map[string]any); ok {
					if sr, _ := respObj["stop_reason"].(string); sr == "tool_call" || sr == "tool_calls" {
						finish = "tool_calls"
					}
				}
				rawChunk = map[string]any{
					"id": fmt.Sprintf("zen-%d", time.Now().UnixNano()), "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": string(req.Model),
					"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
				}
			default:
				continue
			}
			if rawChunk == nil {
				continue
			}
			b, _ := json.Marshal(rawChunk)
			fmt.Fprintf(w, "data: %s\n\n", b)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			if finish, ok := rawChunk["choices"].([]any)[0].(map[string]any)["finish_reason"].(string); ok && finish != "" {
				// actually finish_reason is string, need check
				if finish != "" {
					fmt.Fprintln(w, "data: [DONE]")
					fmt.Fprintln(w, "")
					break
				}
			} else if rawChunk["choices"].([]any)[0].(map[string]any)["finish_reason"] != nil {
				fmt.Fprintln(w, "data: [DONE]")
				fmt.Fprintln(w, "")
				break
			}
			continue
		}
		if strings.HasPrefix(trim, "data: ") {
			data := strings.TrimPrefix(trim, "data: ")
			if data == "[DONE]" {
				fmt.Fprintln(w, "data: [DONE]")
				fmt.Fprintln(w, "")
				break
			}
			// fallback: try to parse as json with type field
			var evt map[string]any
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				continue
			}
			if t, _ := evt["type"].(string); strings.Contains(t, "output_text.delta") {
				delta := extractDelta(evt)
				if delta == "" {
					continue
				}
				chunk := sdk.StreamChunk{
					ID:      fmt.Sprintf("zen-%d", time.Now().UnixNano()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   string(req.Model),
					Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{Role: "assistant", Content: delta}}},
				}
				b, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", b)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
	}
	fmt.Fprintln(w, "data: [DONE]")
	fmt.Fprintln(w, "")
	return nil, scanner.Err()
}

func extractResponsesText(raw map[string]any) string {
	// try output -> content -> text
	if out, ok := raw["output"].([]any); ok {
		for _, item := range out {
			if m, ok := item.(map[string]any); ok {
				if m["type"] == "message" {
					if content, ok := m["content"].([]any); ok {
						for _, c := range content {
							if cm, ok := c.(map[string]any); ok {
								if txt, ok := cm["text"].(string); ok && txt != "" {
									return txt
								}
								if txt, ok := cm["output_text"].(string); ok && txt != "" {
									return txt
								}
							}
						}
					}
				}
			}
		}
	}
	if txt, ok := raw["output_text"].(string); ok {
		return txt
	}
	return ""
}

func extractDelta(evt map[string]any) string {
	// Zen streams response.output_text.delta events
	if t, ok := evt["type"].(string); ok && strings.Contains(t, "output_text.delta") {
		if delta, ok := evt["delta"].(string); ok {
			return delta
		}
		if txt, ok := evt["text"].(string); ok {
			return txt
		}
	}
	// fallback
	if d, ok := evt["delta"].(string); ok {
		return d
	}
	return ""
}

func extractUsage(raw map[string]any, key string) float64 {
	if u, ok := raw["usage"].(map[string]any); ok {
		if v, ok := u[key].(float64); ok {
			return v
		}
	}
	return 0
}

func buildResponsesInput(messages []sdk.ChatMessage) []any {
	var input []any
	for _, m := range messages {
		switch m.Role {
		case "system":
			text := strings.TrimSpace(m.Content)
			if text == "" {
				text = strings.TrimSpace(m.TextContent())
			}
			if text != "" {
				input = append(input, map[string]any{"role": "system", "content": text})
			}
		case "user":
			// Check for tool_result parts (Zed sends tool results as user with tool_result or as tool role)
			hasToolResult := false
			for _, part := range m.ContentParts {
				if part.Type == "tool_result" {
					hasToolResult = true
					input = append(input, map[string]any{"type": "function_call_output", "call_id": part.ToolUseID, "output": part.TextContent()})
				}
			}
			if hasToolResult {
				// also append any text part as separate user message if present
				text := strings.TrimSpace(m.TextContent())
				if text != "" {
					// avoid duplicating tool output as user text
					hasOnlyToolResults := true
					for _, part := range m.ContentParts {
						if part.Type != "tool_result" && strings.TrimSpace(part.TextContent()) != "" {
							hasOnlyToolResults = false
							break
						}
					}
					if !hasOnlyToolResults {
						input = append(input, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
					}
				}
				continue
			}
			text := strings.TrimSpace(m.Content)
			if text == "" {
				text = strings.TrimSpace(m.TextContent())
			}
			if text != "" {
				input = append(input, map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
			}
		case "assistant":
			text := strings.TrimSpace(m.Content)
			if text == "" {
				text = strings.TrimSpace(m.TextContent())
			}
			if text != "" {
				input = append(input, map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
			}
			for _, tc := range m.ToolCalls {
				name := strings.TrimSpace(tc.Function.Name)
				if name == "" {
					continue
				}
				args := strings.TrimSpace(tc.Function.Arguments)
				if args == "" {
					args = "{}"
				}
				input = append(input, map[string]any{
					"type": "function_call", "call_id": tc.ID, "name": name, "arguments": args,
				})
			}
			// also handle ContentParts tool_use
			for _, part := range m.ContentParts {
				if part.Type == "tool_use" && strings.TrimSpace(part.Name) != "" {
					input = append(input, map[string]any{
						"type": "function_call", "call_id": part.ID, "name": part.Name, "arguments": string(part.Input),
					})
				}
			}
		case "tool":
			text := strings.TrimSpace(m.Content)
			if text == "" {
				text = strings.TrimSpace(m.TextContent())
			}
			callID := strings.TrimSpace(m.ToolCallID)
			if callID == "" && len(m.ContentParts) > 0 {
				for _, p := range m.ContentParts {
					if p.ToolUseID != "" {
						callID = p.ToolUseID
						break
					}
				}
			}
			if callID == "" {
				callID = "call_unknown"
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": callID, "output": text})
		}
	}
	return input
}

func buildResponsesTools(tools []sdk.ChatTool) []any {
	if len(tools) == 0 {
		return nil
	}
	var out []any
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		desc := strings.TrimSpace(t.Description)
		params := t.Parameters
		if t.Function != nil {
			if strings.TrimSpace(t.Function.Name) != "" {
				name = strings.TrimSpace(t.Function.Name)
			}
			if strings.TrimSpace(t.Function.Description) != "" {
				desc = strings.TrimSpace(t.Function.Description)
			}
			if len(t.Function.Parameters) > 0 {
				params = t.Function.Parameters
			}
		}
		if name == "" {
			continue
		}
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{
			"type": "function", "name": name, "description": desc, "parameters": params,
		})
	}
	return out
}

func buildResponsesToolChoice(tc any) any {
	if tc == nil {
		return nil
	}
	switch v := tc.(type) {
	case string:
		if v == "auto" || v == "required" || v == "none" {
			return v
		}
		return nil
	case map[string]any:
		if typ, _ := v["type"].(string); typ == "function" {
			if fn, ok := v["function"].(map[string]any); ok {
				if name, _ := fn["name"].(string); name != "" {
					return map[string]any{"type": "function", "function": map[string]any{"name": name}}
				}
			}
		}
	}
	return nil
}

func extractResponsesToolCalls(raw map[string]any) []sdk.ChatToolCall {
	var out []sdk.ChatToolCall
	if output, ok := raw["output"].([]any); ok {
		for _, item := range output {
			m, ok := item.(map[string]any)
			if !ok || m["type"] != "function_call" {
				continue
			}
			name, _ := m["name"].(string)
			if name == "" {
				continue
			}
			args, _ := m["arguments"].(string)
			if args == "" {
				if a, ok := m["arguments"].(map[string]any); ok {
					b, _ := json.Marshal(a)
					args = string(b)
				}
			}
			id, _ := m["call_id"].(string)
			if id == "" {
				id, _ = m["id"].(string)
			}
			if id == "" {
				id = fmt.Sprintf("call_%d", time.Now().UnixNano())
			}
			out = append(out, sdk.ChatToolCall{
				ID: id, Type: "function",
				Function: sdk.ChatToolFunction{Name: name, Arguments: args},
			})
		}
	}
	return out
}

func parseResponsesEvent(event, data, model string) *sdk.StreamChunk {
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return nil
	}
	switch event {
	case "response.output_text.delta":
		delta, _ := payload["delta"].(string)
		if delta == "" {
			delta, _ = payload["text"].(string)
		}
		if delta == "" {
			return nil
		}
		return &sdk.StreamChunk{
			ID: fmt.Sprintf("zen-%d", time.Now().UnixNano()), Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
			Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{Role: "assistant", Content: delta}}},
		}
	case "response.output_item.added":
		if item, ok := payload["item"].(map[string]any); ok && item["type"] == "function_call" {
			name, _ := item["name"].(string)
			id, _ := item["id"].(string)
			if id == "" {
				id, _ = item["call_id"].(string)
			}
			if name != "" {
				return &sdk.StreamChunk{
					ID: fmt.Sprintf("zen-%d", time.Now().UnixNano()), Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
					Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{
						Role: "assistant",
						ToolCalls: []sdk.ChatToolCall{{ID: id, Type: "function", Function: sdk.ChatToolFunction{Name: name, Arguments: ""}}},
					}}},
				}
			}
		}
	case "response.function_call_arguments.delta":
		delta, _ := payload["delta"].(string)
		if delta == "" {
			delta, _ = payload["arguments_delta"].(string)
		}
		if delta != "" {
			return &sdk.StreamChunk{
				ID: fmt.Sprintf("zen-%d", time.Now().UnixNano()), Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
				Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{
					Role: "assistant",
					ToolCalls: []sdk.ChatToolCall{{ID: "", Type: "function", Function: sdk.ChatToolFunction{Arguments: delta}}},
				}}},
			}
		}
	case "response.completed":
		finish := "stop"
		if resp, ok := payload["response"].(map[string]any); ok {
			if sr, _ := resp["stop_reason"].(string); sr == "tool_call" || sr == "tool_calls" {
				finish = "tool_calls"
			}
		}
		return &sdk.StreamChunk{
			ID: fmt.Sprintf("zen-%d", time.Now().UnixNano()), Object: "chat.completion.chunk", Created: time.Now().Unix(), Model: model,
			Choices: []sdk.StreamChunkChoice{{Index: 0, Delta: sdk.ChatMessage{Role: "assistant", Content: ""}, FinishReason: &finish}},
		}
	}
	return nil
}

func (c *Client) ListModels(ctx context.Context, apiKey string) ([]sdk.ModelInfo, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("opencode-zen: create list models request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	addOpenCodeHeaders(httpReq)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, parseUpstreamError(resp.StatusCode, resp.Header, body)
	}

	var upstream upstreamModelsResponse
	if err := json.Unmarshal(body, &upstream); err != nil {
		return nil, fmt.Errorf("opencode-zen: parse models: %w", err)
	}

	infos := make([]sdk.ModelInfo, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		name := strings.TrimSpace(m.ID)
		if name == "" {
			continue
		}
		infos = append(infos, sdk.ModelInfo{
			Name:          name,
			DisplayName:   name,
			ContextWindow: 200000,
			MaxTokens:     32000,
			RPM:           60,
			TPM:           100000,
			RPD:           500,
		})
	}
	return infos, nil
}

func addOpenCodeHeaders(req *http.Request) {
	// Console inference now requires OpenCode client identity for free tier.
	// Mimic opencode/src/session/llm/request.ts headers so allowAnonymous
	// free models pass the "can only be used in OpenCode" gate.
	req.Header.Set("User-Agent", "opencode/1.18.29")
	req.Header.Set("x-opencode-client", "opencode")
	if req.Header.Get("x-opencode-session") == "" {
		req.Header.Set("x-opencode-session", newID())
	}
	if req.Header.Get("x-opencode-request") == "" {
		req.Header.Set("x-opencode-request", newID())
	}
	if req.Header.Get("x-opencode-project") == "" {
		req.Header.Set("x-opencode-project", "proj_llm-router")
	}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
