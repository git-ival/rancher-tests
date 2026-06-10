package cmd

import (
	"context"
	"os"

	"github.com/rancher/tests/internal/agenticqa/llm"
)

// newLLMClient creates an LLM client using the persistent flags.
func newLLMClient(ctx context.Context, model string) (*llm.Client, error) {
	return llm.NewClient(ctx, llm.Config{
		Provider:       provider,
		Model:          model,
		APIKey:         os.Getenv("CLAUDE_API_KEY"),
		VertexProject:  vertexProject,
		VertexLocation: vertexLocation,
	})
}
