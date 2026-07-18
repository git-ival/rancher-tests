package triage

import (
	"regexp"

	"github.com/rancher/tests/internal/agenticqa/types"
)

// PatternRule maps a regex to a classification.
type PatternRule struct {
	Pattern     *regexp.Regexp
	Category    string // values are string forms of types.TriageClassification
	Confidence  string // values are string forms of types.TriageConfidence
	Description string
}

// Convenience aliases so existing intra-package code can refer to these without
// importing types. External callers should use types.Class*/types.Confidence*.
const (
	ClassProductDefect     = string(types.ClassProductDefect)
	ClassTestDefect        = string(types.ClassTestDefect)
	ClassConfigEnvironment = string(types.ClassConfigEnvironment)
	ClassPassed            = string(types.ClassPassed)
	ClassUnknown           = string(types.ClassUnknown)
)

// EnvPatterns are patterns that indicate infrastructure/environment issues.
var EnvPatterns = []PatternRule{
	{regexp.MustCompile(`(?i)context deadline exceeded`), ClassConfigEnvironment, string(types.ConfidenceHigh), "Infrastructure timeout"},
	{regexp.MustCompile(`(?i)i/o timeout`), ClassConfigEnvironment, string(types.ConfidenceHigh), "Network I/O timeout"},
	{regexp.MustCompile(`(?i)connection refused`), ClassConfigEnvironment, string(types.ConfidenceHigh), "Service unreachable"},
	{regexp.MustCompile(`(?i)timed out waiting for`), ClassConfigEnvironment, string(types.ConfidenceHigh), "Kubernetes wait timeout"},
	{regexp.MustCompile(`(?i)(ThrottlingException|RequestLimitExceeded|Rate exceeded|Too Many Requests|429)`), ClassConfigEnvironment, string(types.ConfidenceHigh), "Cloud provider rate limiting"},
	{regexp.MustCompile(`(?i)(insufficient.?capacity|quota exceeded|InsufficientInstanceCapacity)`), ClassConfigEnvironment, string(types.ConfidenceHigh), "Cloud resource exhaustion"},
	{regexp.MustCompile(`(?i)no such host`), ClassConfigEnvironment, string(types.ConfidenceHigh), "DNS resolution failure"},
	{regexp.MustCompile(`(?i)(x509|certificate|tls.*handshake)`), ClassConfigEnvironment, string(types.ConfidenceMedium), "TLS/certificate issue"},
	{regexp.MustCompile(`(?i)(ssh.*connection|ssh.*timeout|connection reset by peer)`), ClassConfigEnvironment, string(types.ConfidenceMedium), "SSH connectivity failure"},
	{regexp.MustCompile(`(?i)(ImagePull|ErrImagePull|ImagePullBackOff)`), ClassConfigEnvironment, string(types.ConfidenceMedium), "Container image pull failure"},
	{regexp.MustCompile(`(?i)etcd.*(leader|election|timeout|unavailable)`), ClassConfigEnvironment, string(types.ConfidenceMedium), "Etcd cluster instability"},
	{regexp.MustCompile(`(?i)node.*(NotReady|not ready)`), ClassConfigEnvironment, string(types.ConfidenceMedium), "Kubernetes node not ready"},
}

// TestDefectPatterns indicate the test code itself is broken.
var TestDefectPatterns = []PatternRule{
	{regexp.MustCompile(`(?i)nil pointer dereference`), ClassTestDefect, string(types.ConfidenceHigh), "Nil pointer in test code"},
	{regexp.MustCompile(`(?i)index out of range`), ClassTestDefect, string(types.ConfidenceHigh), "Index out of range in test"},
	{regexp.MustCompile(`(?i)cannot find provider`), ClassTestDefect, string(types.ConfidenceMedium), "Provider not configured in test"},
	{regexp.MustCompile(`(?i)unable to find test case`), ClassTestDefect, string(types.ConfidenceMedium), "Missing Qase schema entry"},
	{regexp.MustCompile(`(?i)(compilation|build constraint|does not compile)`), ClassTestDefect, string(types.ConfidenceHigh), "Test compilation error"},
	// Groovy/Declarative Pipeline parser failures — the Jenkinsfile or shared-library
	// script failed to compile on the Jenkins controller before any stage ran.
	{regexp.MustCompile(`MultipleCompilationErrorsException`), ClassTestDefect, string(types.ConfidenceHigh), "Groovy compilation error in pipeline script"},
	{regexp.MustCompile(`(?i)WorkflowScript.*expecting`), ClassTestDefect, string(types.ConfidenceHigh), "Declarative Pipeline syntax error"},
	{regexp.MustCompile(`(?i)startup failed:.*\d+ error`), ClassTestDefect, string(types.ConfidenceHigh), "Groovy script startup failure"},
	{regexp.MustCompile(`(?i)Jenkinsfile validation failed`), ClassTestDefect, string(types.ConfidenceHigh), "Jenkinsfile failed pre-trigger lint"},
}

// ProductDefectPatterns indicate the Rancher product has a bug.
var ProductDefectPatterns = []PatternRule{
	{regexp.MustCompile(`(?i)(cluster.*error|cluster.*failed).*(active|ready)`), ClassProductDefect, string(types.ConfidenceHigh), "Cluster entered error state"},
	{regexp.MustCompile(`(?i)admission webhook.*denied`), ClassProductDefect, string(types.ConfidenceHigh), "Webhook admission rejection"},
	{regexp.MustCompile(`(?i)(500 Internal Server Error|status.?code.?500)`), ClassProductDefect, string(types.ConfidenceHigh), "Server 500 error"},
	{regexp.MustCompile(`(?i)forbidden.*rbac`), ClassProductDefect, string(types.ConfidenceMedium), "RBAC permission denied"},
	{regexp.MustCompile(`(?i)failed to create.*resource`), ClassProductDefect, string(types.ConfidenceMedium), "Resource creation failure"},
}
