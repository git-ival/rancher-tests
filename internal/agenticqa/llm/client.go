package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"github.com/avast/retry-go/v4"
	"github.com/sirupsen/logrus"
)

const (
	// ProviderVertexAI is the default provider using Google Cloud Vertex AI.
	ProviderVertexAI = "vertex-ai"
	// ProviderClaudeDirect uses the Anthropic API directly.
	ProviderClaudeDirect = "claude-direct"

	defaultRetryAttempts = 5
	minRetryDelay        = 2 * time.Second
	maxRetryDelay        = 60 * time.Second
)

// Client wraps the Anthropic Messages API for both Direct and Vertex AI providers.
type Client struct {
	client   anthropic.Client
	model    string
	provider string

	totalInputTokens  atomic.Int64
	totalOutputTokens atomic.Int64
}

// Config for creating a new Client.
type Config struct {
	Provider       string // "vertex-ai" (default) or "claude-direct"
	Model          string // e.g. "claude-sonnet-4-6-20250514"
	APIKey         string // required for claude-direct (CLAUDE_API_KEY)
	VertexProject  string // required for vertex-ai (GCP project ID)
	VertexLocation string // GCP location (e.g. "global")
}

// NewClient creates a new LLM client configured for the specified provider.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Provider == "" {
		cfg.Provider = ProviderVertexAI
	}

	var opts []option.RequestOption

	switch cfg.Provider {
	case ProviderVertexAI:
		if cfg.VertexProject == "" {
			return nil, fmt.Errorf("llm: VertexProject is required for provider %q", cfg.Provider)
		}
		if cfg.VertexLocation == "" {
			return nil, fmt.Errorf("llm: VertexLocation is required for provider %q", cfg.Provider)
		}
		opts = append(opts,
			vertex.WithGoogleAuth(ctx, cfg.VertexLocation, cfg.VertexProject,
				"https://www.googleapis.com/auth/cloud-platform",
			),
		)
	case ProviderClaudeDirect:
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("llm: APIKey is required for provider %q", cfg.Provider)
		}
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q (supported: %q, %q)", cfg.Provider, ProviderVertexAI, ProviderClaudeDirect)
	}

	client := anthropic.NewClient(opts...)

	return &Client{
		client:   client,
		model:    cfg.Model,
		provider: cfg.Provider,
	}, nil
}

// Complete sends a single-turn message and returns the text response.
func (c *Client) Complete(ctx context.Context, systemPrompt, userMessage string, maxTokens int) (string, error) {
	var response *anthropic.Message

	err := retry.Do(
		func() error {
			var reqErr error
			response, reqErr = c.client.Messages.New(ctx, anthropic.MessageNewParams{
				Model:     anthropic.Model(c.model),
				MaxTokens: int64(maxTokens),
				System: []anthropic.TextBlockParam{
					{Text: systemPrompt},
				},
				Messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock(userMessage)),
				},
				Temperature: anthropic.Float(0.3),
			})
			return reqErr
		},
		retry.Context(ctx),
		retry.Attempts(defaultRetryAttempts),
		retry.Delay(minRetryDelay),
		retry.MaxDelay(maxRetryDelay),
		retry.DelayType(retry.BackOffDelay),
		retry.RetryIf(isRetryableError),
		retry.OnRetry(func(n uint, err error) {
			logrus.WithFields(logrus.Fields{
				"attempt": n + 1,
				"error":   err.Error(),
			}).Warn("LLM request failed, retrying")
		}),
	)
	if err != nil {
		return "", fmt.Errorf("llm: messages.New failed after retries: %w", err)
	}

	// Record token usage.
	c.totalInputTokens.Add(response.Usage.InputTokens)
	c.totalOutputTokens.Add(response.Usage.OutputTokens)

	// Extract text from response content blocks.
	var parts []string
	for _, block := range response.Content {
		if block.Type == "text" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("llm: response contained no text blocks")
	}

	return strings.Join(parts, ""), nil
}

// CompleteJSON sends a message, instructs JSON-only output, and unmarshals the response.
func (c *Client) CompleteJSON(ctx context.Context, systemPrompt, userMessage string, maxTokens int, result any) error {
	augmentedPrompt := systemPrompt + "\n\nIMPORTANT: Respond ONLY with valid JSON. No markdown fences, no commentary."

	raw, err := c.Complete(ctx, augmentedPrompt, userMessage, maxTokens)
	if err != nil {
		return err
	}

	cleaned := stripMarkdownFences(raw)

	if err := json.Unmarshal([]byte(cleaned), result); err != nil {
		return fmt.Errorf("llm: failed to unmarshal JSON response: %w (raw response: %.500s)", err, raw)
	}

	return nil
}

// TotalInputTokens returns cumulative input token count.
func (c *Client) TotalInputTokens() int64 {
	return c.totalInputTokens.Load()
}

// TotalOutputTokens returns cumulative output token count.
func (c *Client) TotalOutputTokens() int64 {
	return c.totalOutputTokens.Load()
}

// isRetryableError returns true for rate-limit and server errors that are worth retrying.
func isRetryableError(err error) bool {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		// Retry on 429 (rate limit) and 5xx (server errors).
		if apiErr.StatusCode == 429 || apiErr.StatusCode >= 500 {
			return true
		}
	}
	return false
}

// stripMarkdownFences removes optional markdown code fences from a JSON string.
func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Remove opening fence (e.g. ```json or just ```)
		if idx := strings.Index(s, "\n"); idx != -1 {
			s = s[idx+1:]
		}
		// Remove closing fence
		if strings.HasSuffix(s, "```") {
			s = s[:len(s)-3]
		}
		s = strings.TrimSpace(s)
	}
	return s
}
