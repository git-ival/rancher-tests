package k3k

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	sigsyaml "sigs.k8s.io/yaml"
)

// SpecContext holds the per-cluster values available to clusterSpec templates.
//
// A single clusterSpec block is shared by every cluster in a batch, so fields that must be
// unique per cluster (Ingress hostnames, fixed node ports) are written as template
// expressions and resolved here.
type SpecContext struct {
	// Name is the generated cluster name, for example k3k-ci-001.
	Name string
	// Namespace is the k3k namespace for the cluster, for example k3k-k3k-ci-001.
	Namespace string
	// Batch is the batch prefix shared by every cluster in the run.
	Batch string
	// Index is the one-based ordinal of the cluster within the batch.
	Index int
	// Index0 is the zero-based ordinal of the cluster within the batch.
	Index0 int
	// Target is the host cluster name this cluster is assigned to.
	Target string
	// TargetType is the Rancher cluster type of the host cluster.
	TargetType string
	// KubeconfigServer is the reachable host address used for the generated kubeconfig.
	KubeconfigServer string
}

// specFuncs is the template function map exposed to clusterSpec templates. It is intentionally
// small and explicit rather than a general-purpose library so the supported surface stays
// documented and testable.
var specFuncs = template.FuncMap{
	"add":   func(a, b int) int { return a + b },
	"sub":   func(a, b int) int { return a - b },
	"mul":   func(a, b int) int { return a * b },
	"mod":   func(a, b int) (int, error) { return safeMod(a, b) },
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
}

func safeMod(a, b int) (int, error) {
	if b == 0 {
		return 0, fmt.Errorf("mod by zero")
	}
	return a % b, nil
}

// SpecContextFor builds the template context for one cluster in the batch.
//
// Index is derived from the persisted cluster name suffix rather than from iteration order so
// that a resumed run renders a byte-identical manifest for the same cluster.
func SpecContextFor(item *ClusterState, target Target, config *Config) SpecContext {
	index := indexFromName(item.Name, config.Batch)
	targetType := item.TargetType
	if targetType == "" {
		targetType = target.Type
	}
	return SpecContext{
		Name:             item.Name,
		Namespace:        item.Namespace,
		Batch:            config.Batch,
		Index:            index,
		Index0:           index - 1,
		Target:           target.Name,
		TargetType:       targetType,
		KubeconfigServer: target.KubeconfigServer,
	}
}

// indexFromName recovers the one-based ordinal encoded in a generated cluster name.
// Names are produced as "<batch>-%03d" by LoadOrCreateState. A name that does not follow the
// convention yields 1 so templating still resolves rather than failing the run.
func indexFromName(name, batch string) int {
	suffix := strings.TrimPrefix(name, batch+"-")
	if suffix == name {
		return 1
	}
	index, err := strconv.Atoi(suffix)
	if err != nil || index < 1 {
		return 1
	}
	return index
}

// sampleSpecContext is the synthetic context used to validate the clusterSpec at load time,
// before any cluster state exists. It matches the first cluster of the batch.
func sampleSpecContext(config *Config) SpecContext {
	name := fmt.Sprintf("%s-%03d", config.Batch, 1)
	context := SpecContext{
		Name:      name,
		Namespace: "k3k-" + name,
		Batch:     config.Batch,
		Index:     1,
		Index0:    0,
	}
	if len(config.Targets) > 0 {
		context.Target = config.Targets[0].Name
		context.TargetType = config.Targets[0].Type
		context.KubeconfigServer = config.Targets[0].KubeconfigServer
	}
	return context
}

// RenderSpec executes the raw clusterSpec YAML as a template and strictly decodes the result.
//
// Templating runs before decoding so that expressions are usable in non-string fields such as
// expose.nodePort.serverPort, which is an *int32 and would fail to decode if the template
// text reached the decoder. Strict decoding rejects fields outside the supported subset.
func RenderSpec(rawSpecYAML []byte, context SpecContext) (*ClusterSpec, error) {
	rendered, err := renderSpecYAML(rawSpecYAML, context)
	if err != nil {
		return nil, err
	}
	return decodeSpec(rendered)
}

// renderSpecYAML executes the clusterSpec template and returns the resulting YAML text.
func renderSpecYAML(rawSpecYAML []byte, context SpecContext) ([]byte, error) {
	if len(bytes.TrimSpace(rawSpecYAML)) == 0 {
		return nil, nil
	}

	tmpl, err := template.New("clusterSpec").
		Funcs(specFuncs).
		Option("missingkey=error").
		Parse(string(rawSpecYAML))
	if err != nil {
		return nil, fmt.Errorf("parse clusterSpec template: %w", err)
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, context); err != nil {
		return nil, fmt.Errorf("render clusterSpec template for %s: %w", context.Name, err)
	}
	return rendered.Bytes(), nil
}

// decodeSpec strictly decodes rendered clusterSpec YAML, rejecting unsupported fields.
func decodeSpec(rendered []byte) (*ClusterSpec, error) {
	spec := &ClusterSpec{}
	if len(bytes.TrimSpace(rendered)) == 0 {
		return spec, nil
	}
	if err := sigsyaml.UnmarshalStrict(rendered, spec); err != nil {
		return nil, fmt.Errorf("parse clusterSpec: %w", err)
	}
	return spec, nil
}
