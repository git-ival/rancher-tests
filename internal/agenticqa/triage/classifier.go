package triage

import (
	"context"
	"fmt"

	"github.com/rancher/tests/internal/agenticqa/llm"
	"github.com/sirupsen/logrus"
)

// Classification is the result of classifying a single test failure.
type Classification struct {
	TestName            string `json:"test_name"`
	Package             string `json:"package"`
	Error               string `json:"error"`
	Category            string `json:"classification"`
	Confidence          string `json:"confidence"`
	Evidence            string `json:"evidence"`
	PatternMatched      string `json:"pattern_matched,omitempty"`
	RecommendedRepo     string `json:"recommended_repo,omitempty"`
	RecommendedSeverity string `json:"recommended_severity,omitempty"`
	RecommendedAction   string `json:"recommended_action,omitempty"`
}

// Classifier classifies test failures using pattern matching and LLM.
type Classifier struct {
	llmClient *llm.Client
}

// NewClassifier creates a classifier with an optional LLM client for fallback.
func NewClassifier(llmClient *llm.Client) *Classifier {
	return &Classifier{llmClient: llmClient}
}

// ClassifyByPattern attempts to classify an error using regex patterns only.
// Returns nil if no pattern matches.
func (c *Classifier) ClassifyByPattern(testName, pkg, errorText string) *Classification {
	// Check EnvPatterns first (most common in CI).
	for _, rule := range EnvPatterns {
		if rule.Pattern.MatchString(errorText) {
			return &Classification{
				TestName:       testName,
				Package:        pkg,
				Error:          errorText,
				Category:       rule.Category,
				Confidence:     rule.Confidence,
				Evidence:       rule.Description,
				PatternMatched: rule.Pattern.String(),
			}
		}
	}

	// Then TestDefectPatterns.
	for _, rule := range TestDefectPatterns {
		if rule.Pattern.MatchString(errorText) {
			return &Classification{
				TestName:        testName,
				Package:         pkg,
				Error:           errorText,
				Category:        rule.Category,
				Confidence:      rule.Confidence,
				Evidence:        rule.Description,
				PatternMatched:  rule.Pattern.String(),
				RecommendedRepo: "rancher/tests",
			}
		}
	}

	// Then ProductDefectPatterns.
	for _, rule := range ProductDefectPatterns {
		if rule.Pattern.MatchString(errorText) {
			return &Classification{
				TestName:        testName,
				Package:         pkg,
				Error:           errorText,
				Category:        rule.Category,
				Confidence:      rule.Confidence,
				Evidence:        rule.Description,
				PatternMatched:  rule.Pattern.String(),
				RecommendedRepo: "rancher/rancher",
			}
		}
	}

	return nil
}

// llmClassification is the JSON schema the LLM is asked to produce.
type llmClassification struct {
	Classification string `json:"classification"`
	Confidence     string `json:"confidence"`
	Evidence       string `json:"evidence"`
	Severity       string `json:"recommended_severity"`
	Action         string `json:"recommended_action"`
}

const llmSystemPrompt = `You are a test failure triage classifier for the Rancher Kubernetes management platform.

Classify the following test failure into exactly one of these categories:

1. "product_defect" — The Rancher product (or one of its components) has a bug. Examples:
   - API returning unexpected errors (500s, incorrect responses)
   - Resources not being created/updated/deleted correctly
   - RBAC or admission webhook misbehavior
   - Cluster provisioning failures due to Rancher logic errors

2. "test_defect" — The test code itself is broken. Examples:
   - Nil pointer dereferences or index out of range in test helpers
   - Missing test configuration or provider setup
   - Compilation errors in test code
   - Incorrect test assertions or stale test expectations

3. "config_environment" — Infrastructure, environment, or configuration issues. Examples:
   - Timeouts (context deadline exceeded, i/o timeout)
   - Cloud provider rate limiting or capacity issues
   - DNS failures, TLS errors, SSH connectivity issues
   - Image pull failures, node not ready

Respond with a JSON object containing:
- "classification": one of "product_defect", "test_defect", "config_environment"
- "confidence": "high", "medium", or "low"
- "evidence": a brief explanation of why this classification was chosen
- "recommended_severity": "critical", "major", "minor", or "trivial"
- "recommended_action": a short recommended next step`

// ClassifyWithLLM uses the LLM to classify a failure that didn't match patterns.
func (c *Classifier) ClassifyWithLLM(ctx context.Context, testName, pkg, errorText, stackTrace string) (*Classification, error) {
	if c.llmClient == nil {
		return nil, fmt.Errorf("triage: LLM client is nil; cannot classify without patterns or LLM")
	}

	userMessage := fmt.Sprintf("Test: %s\nPackage: %s\n\nError:\n%s", testName, pkg, errorText)
	if stackTrace != "" {
		userMessage += fmt.Sprintf("\n\nStack Trace:\n%s", stackTrace)
	}

	var result llmClassification
	if err := c.llmClient.CompleteJSON(ctx, llmSystemPrompt, userMessage, 1024, &result); err != nil {
		return nil, fmt.Errorf("triage: LLM classification failed: %w", err)
	}

	logrus.WithFields(logrus.Fields{
		"test":           testName,
		"classification": result.Classification,
		"confidence":     result.Confidence,
	}).Debug("LLM triage classification complete")

	repo := repoForCategory(result.Classification)

	return &Classification{
		TestName:            testName,
		Package:             pkg,
		Error:               errorText,
		Category:            result.Classification,
		Confidence:          result.Confidence,
		Evidence:            result.Evidence,
		RecommendedRepo:     repo,
		RecommendedSeverity: result.Severity,
		RecommendedAction:   result.Action,
	}, nil
}

// repoForCategory maps a classification category to the recommended repository.
func repoForCategory(category string) string {
	switch category {
	case ClassProductDefect:
		return "rancher/rancher"
	case ClassTestDefect:
		return "rancher/tests"
	default:
		return ""
	}
}
