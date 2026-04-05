package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var openaiHTTPClient = NewHTTPClientWithTimeout()

type OpenAIProvider struct {
	apiKey    string
	model     string
	maxTokens int
	baseURL   string
}

func NewOpenAIProvider(apiKey, model string, maxTokens int, baseURL string) *OpenAIProvider {
	if model == "" {
		model = "gpt-4o"
	}
	if maxTokens <= 0 {
		maxTokens = 16384
	}
	// Ensure baseURL doesn't have trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	return &OpenAIProvider{
		apiKey:    apiKey,
		model:     model,
		maxTokens: maxTokens,
		baseURL:   baseURL,
	}
}

// openaiRequest is the Chat Completions API request body.
type openaiRequest struct {
	Model       string          `json:"model"`
	Messages    []openaiMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiResponse is the Chat Completions API response body.
type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Model string `json:"model"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (o *OpenAIProvider) AnalyzeChat(ctx context.Context, systemPrompt string, chatTranscript string) (AIResponse, error) {
	return withRetry(ctx, "openai", func() (AIResponse, error) {
		reqBody := openaiRequest{
			Model: o.model,
			Messages: []openaiMessage{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: chatTranscript},
			},
			MaxTokens: o.maxTokens,
		}

		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return AIResponse{}, fmt.Errorf("openai marshal error: %w", err)
		}

		// Build URL: baseURL + /chat/completions
		url := o.baseURL + "/chat/completions"

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return AIResponse{}, fmt.Errorf("openai request error: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+o.apiKey)

		resp, err := openaiHTTPClient.Do(req)
		if err != nil {
			return AIResponse{}, fmt.Errorf("openai api error: %w", err)
		}
		defer resp.Body.Close()

		respBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			return AIResponse{}, fmt.Errorf("openai read error: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			return AIResponse{}, fmt.Errorf("openai api error (status %d): %s", resp.StatusCode, string(respBytes))
		}

		var result openaiResponse
		if err := json.Unmarshal(respBytes, &result); err != nil {
			return AIResponse{}, fmt.Errorf("openai unmarshal error: %w", err)
		}

		if result.Error != nil {
			return AIResponse{}, fmt.Errorf("openai api error: %s", result.Error.Message)
		}

		if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
			return AIResponse{}, fmt.Errorf("openai api returned empty content")
		}

		return AIResponse{
			Content:      result.Choices[0].Message.Content,
			InputTokens:  result.Usage.PromptTokens,
			OutputTokens: result.Usage.CompletionTokens,
			Model:        result.Model,
			Provider:     "openai",
		}, nil
	})
}

func (o *OpenAIProvider) AnalyzeChatBatch(ctx context.Context, systemPrompt string, items []BatchItem) (AIResponse, error) {
	batchPrompt := WrapBatchPrompt(systemPrompt, len(items))
	batchTranscript := FormatBatchTranscript(items)
	return o.AnalyzeChat(ctx, batchPrompt, batchTranscript)
}
