package triage

import "regexp"

// Category constants.
const (
	ClassProductDefect     = "product_defect"
	ClassTestDefect        = "test_defect"
	ClassConfigEnvironment = "config_environment"
)

// PatternRule maps a regex to a classification.
type PatternRule struct {
	Pattern     *regexp.Regexp
	Category    string // ClassProductDefect, ClassTestDefect, ClassConfigEnvironment
	Confidence  string // "high", "medium", "low"
	Description string
}

// EnvPatterns are patterns that indicate infrastructure/environment issues.
var EnvPatterns = []PatternRule{
	{regexp.MustCompile(`(?i)context deadline exceeded`), ClassConfigEnvironment, "high", "Infrastructure timeout"},
	{regexp.MustCompile(`(?i)i/o timeout`), ClassConfigEnvironment, "high", "Network I/O timeout"},
	{regexp.MustCompile(`(?i)connection refused`), ClassConfigEnvironment, "high", "Service unreachable"},
	{regexp.MustCompile(`(?i)timed out waiting for`), ClassConfigEnvironment, "high", "Kubernetes wait timeout"},
	{regexp.MustCompile(`(?i)(ThrottlingException|RequestLimitExceeded|Rate exceeded|Too Many Requests|429)`), ClassConfigEnvironment, "high", "Cloud provider rate limiting"},
	{regexp.MustCompile(`(?i)(insufficient.?capacity|quota exceeded|InsufficientInstanceCapacity)`), ClassConfigEnvironment, "high", "Cloud resource exhaustion"},
	{regexp.MustCompile(`(?i)no such host`), ClassConfigEnvironment, "high", "DNS resolution failure"},
	{regexp.MustCompile(`(?i)(x509|certificate|tls.*handshake)`), ClassConfigEnvironment, "medium", "TLS/certificate issue"},
	{regexp.MustCompile(`(?i)(ssh.*connection|ssh.*timeout|connection reset by peer)`), ClassConfigEnvironment, "medium", "SSH connectivity failure"},
	{regexp.MustCompile(`(?i)(ImagePull|ErrImagePull|ImagePullBackOff)`), ClassConfigEnvironment, "medium", "Container image pull failure"},
	{regexp.MustCompile(`(?i)etcd.*(leader|election|timeout|unavailable)`), ClassConfigEnvironment, "medium", "Etcd cluster instability"},
	{regexp.MustCompile(`(?i)node.*(NotReady|not ready)`), ClassConfigEnvironment, "medium", "Kubernetes node not ready"},
}

// TestDefectPatterns indicate the test code itself is broken.
var TestDefectPatterns = []PatternRule{
	{regexp.MustCompile(`(?i)nil pointer dereference`), ClassTestDefect, "high", "Nil pointer in test code"},
	{regexp.MustCompile(`(?i)index out of range`), ClassTestDefect, "high", "Index out of range in test"},
	{regexp.MustCompile(`(?i)cannot find provider`), ClassTestDefect, "medium", "Provider not configured in test"},
	{regexp.MustCompile(`(?i)unable to find test case`), ClassTestDefect, "medium", "Missing Qase schema entry"},
	{regexp.MustCompile(`(?i)(compilation|build constraint|does not compile)`), ClassTestDefect, "high", "Test compilation error"},
}

// ProductDefectPatterns indicate the Rancher product has a bug.
var ProductDefectPatterns = []PatternRule{
	{regexp.MustCompile(`(?i)(cluster.*error|cluster.*failed).*(active|ready)`), ClassProductDefect, "high", "Cluster entered error state"},
	{regexp.MustCompile(`(?i)admission webhook.*denied`), ClassProductDefect, "high", "Webhook admission rejection"},
	{regexp.MustCompile(`(?i)(500 Internal Server Error|status.?code.?500)`), ClassProductDefect, "high", "Server 500 error"},
	{regexp.MustCompile(`(?i)forbidden.*rbac`), ClassProductDefect, "medium", "RBAC permission denied"},
	{regexp.MustCompile(`(?i)failed to create.*resource`), ClassProductDefect, "medium", "Resource creation failure"},
}
