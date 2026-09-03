package opencodezen

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/TheSlopMachine/llm-router-sdk"
)

func parseUpstreamError(statusCode int, headers http.Header, body []byte) error {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	var payload upstreamErrorResponse
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Error.Message) != "" {
		message = strings.TrimSpace(payload.Error.Message)
	} else {
		// try generic {"error":"..."} shape
		var alt map[string]any
		if err := json.Unmarshal(body, &alt); err == nil {
			if m, ok := alt["message"].(string); ok && m != "" {
				message = m
			} else if e, ok := alt["error"].(string); ok && e != "" {
				message = e
			}
		}
	}

	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &sdk.ProviderError{StatusCode: statusCode, Message: message, Type: sdk.ErrorTypeAuth}
	case http.StatusTooManyRequests:
		retryAfter := retryAfterFromHeaders(headers)
		if retryAfter == nil {
			t := time.Now().Add(time.Minute)
			retryAfter = &t
		}
		if strings.Contains(strings.ToLower(message), "quota") {
			return &sdk.ProviderError{StatusCode: statusCode, Message: message, Type: sdk.ErrorTypeQuotaExceeded, RetryAfter: retryAfter}
		}
		return &sdk.ProviderError{StatusCode: statusCode, Message: message, Type: sdk.ErrorTypeRateLimit, RetryAfter: retryAfter}
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return &sdk.ProviderError{StatusCode: statusCode, Message: message, Type: sdk.ErrorTypeTimeout}
	default:
		if statusCode >= 500 {
			return &sdk.ProviderError{StatusCode: statusCode, Message: message, Type: sdk.ErrorTypeUpstream}
		}
		return fmt.Errorf("opencode-zen: upstream error (%d): %s", statusCode, message)
	}
}

func retryAfterFromHeaders(headers http.Header) *time.Time {
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		t := time.Now().Add(time.Duration(seconds) * time.Second)
		return &t
	}
	if parsed, err := http.ParseTime(raw); err == nil {
		return &parsed
	}
	return nil
}
