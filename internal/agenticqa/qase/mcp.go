package qase

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sirupsen/logrus"
)

// MCPClient wraps the Qase MCP server for use by the agentic pipeline.
type MCPClient struct {
	serverURL string
}

// NewMCPClient creates an MCP client. If serverURL is empty, all methods return gracefully.
func NewMCPClient(serverURL string) *MCPClient {
	return &MCPClient{serverURL: serverURL}
}

// IsConfigured returns true if a server URL is set.
func (m *MCPClient) IsConfigured() bool {
	return m.serverURL != ""
}

// CallTool calls a tool on the remote MCP server.
// Returns the parsed JSON result or an error.
// Each invocation opens a fresh connection (CI-friendly, stateless).
func (m *MCPClient) CallTool(ctx context.Context, toolName string, args map[string]any) (map[string]any, error) {
	if !m.IsConfigured() {
		return nil, fmt.Errorf("MCP client not configured: no server URL")
	}

	transport, err := m.newTransport()
	if err != nil {
		return nil, fmt.Errorf("creating MCP transport: %w", err)
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "agentic-qa",
		Version: "v1.0.0",
	}, nil)

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to MCP server: %w", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			logrus.WithError(closeErr).Debug("closing MCP session")
		}
	}()

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      toolName,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("calling MCP tool %q: %w", toolName, err)
	}

	if result.IsError {
		return nil, fmt.Errorf("MCP tool %q returned error: %s", toolName, extractText(result))
	}

	text := extractText(result)
	if text == "" {
		return nil, fmt.Errorf("MCP tool %q returned no text content", toolName)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, fmt.Errorf("parsing MCP tool %q response: %w", toolName, err)
	}
	return parsed, nil
}

// newTransport detects the transport type from the URL suffix and returns the
// appropriate MCP transport.
func (m *MCPClient) newTransport() (mcp.Transport, error) {
	switch {
	case strings.HasSuffix(m.serverURL, "/sse"):
		return &mcp.SSEClientTransport{
			Endpoint: m.serverURL,
		}, nil
	case strings.HasSuffix(m.serverURL, "/mcp"):
		return &mcp.StreamableClientTransport{
			Endpoint:   m.serverURL,
			MaxRetries: -1, // disable retries for single-shot calls
		}, nil
	default:
		// Default to streamable HTTP.
		return &mcp.StreamableClientTransport{
			Endpoint:   m.serverURL,
			MaxRetries: -1,
		}, nil
	}
}

// extractText returns the text from the first TextContent item in a CallToolResult.
func extractText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// CreateTestRun creates a test run via the MCP server.
func (m *MCPClient) CreateTestRun(ctx context.Context, project, title, description string) (int, error) {
	result, err := m.CallTool(ctx, "qase_create_run", map[string]any{
		"code":        project,
		"title":       title,
		"description": description,
	})
	if err != nil {
		return 0, fmt.Errorf("creating test run via MCP: %w", err)
	}

	id, ok := result["id"]
	if !ok {
		return 0, fmt.Errorf("MCP create test run: missing id in response")
	}
	idFloat, ok := id.(float64)
	if !ok {
		return 0, fmt.Errorf("MCP create test run: id is not a number")
	}
	return int(idFloat), nil
}

// DeleteTestRun deletes a test run via the MCP server.
func (m *MCPClient) DeleteTestRun(ctx context.Context, project string, runID int) error {
	_, err := m.CallTool(ctx, "qase_delete_run", map[string]any{
		"code": project,
		"id":   runID,
	})
	if err != nil {
		return fmt.Errorf("deleting test run via MCP: %w", err)
	}
	return nil
}

// CreateDefect creates a defect via the MCP server.
func (m *MCPClient) CreateDefect(ctx context.Context, project, title, severity, actualResult string) (int, error) {
	result, err := m.CallTool(ctx, "qase_create_defect", map[string]any{
		"code":          project,
		"title":         title,
		"severity":      severity,
		"actual_result": actualResult,
	})
	if err != nil {
		return 0, fmt.Errorf("creating defect via MCP: %w", err)
	}

	id, ok := result["id"]
	if !ok {
		return 0, fmt.Errorf("MCP create defect: missing id in response")
	}
	idFloat, ok := id.(float64)
	if !ok {
		return 0, fmt.Errorf("MCP create defect: id is not a number")
	}
	return int(idFloat), nil
}

// DeleteDefect deletes a defect via the MCP server.
func (m *MCPClient) DeleteDefect(ctx context.Context, project string, defectID int) error {
	_, err := m.CallTool(ctx, "qase_delete_defect", map[string]any{
		"code": project,
		"id":   defectID,
	})
	if err != nil {
		return fmt.Errorf("deleting defect via MCP: %w", err)
	}
	return nil
}
