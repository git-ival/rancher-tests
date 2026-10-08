package k3k

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"

	sigsyaml "sigs.k8s.io/yaml"
)

// GenerateOptions describes the configuration file to emit.
type GenerateOptions struct {
	Batch                string
	Count                int
	Parallelism          int
	PerTargetParallelism int
	CreateTimeout        string
	ArtifactsDir         string
	Rancher              RancherConfig
	Targets              []Target
	DiscoverTargets      *TargetDiscovery
	// ClusterSpec holds the clusterSpec values the user supplied. Keys present here are
	// emitted live; every other supported field is emitted commented out.
	ClusterSpec map[string]any
}

// GenerateConfig renders an annotated k3kbulk configuration file.
//
// Every supported clusterSpec field appears in the output. Fields the user supplied are
// emitted as live YAML; the rest are emitted as commented placeholders carrying their
// description, default, and permitted values, so an operator can enable one by deleting the
// comment marker.
func GenerateConfig(options GenerateOptions) ([]byte, error) {
	var out bytes.Buffer

	out.WriteString("# k3kbulk configuration.\n")
	out.WriteString("#\n")
	out.WriteString("# Commented clusterSpec fields below are supported but unset. Uncomment one to enable it.\n")
	out.WriteString("# Paths are resolved relative to this file.\n")
	out.WriteString("#\n")
	out.WriteString("# clusterSpec is a Go text/template evaluated once per cluster, so a single block can\n")
	out.WriteString("# produce per-cluster values. Available variables:\n")
	out.WriteString("#   .Name              generated cluster name, e.g. k3k-ci-001\n")
	out.WriteString("#   .Namespace         k3k namespace, e.g. k3k-k3k-ci-001\n")
	out.WriteString("#   .Batch             batch prefix\n")
	out.WriteString("#   .Index / .Index0   one-based / zero-based ordinal within the batch\n")
	out.WriteString("#   .Target            assigned host cluster name\n")
	out.WriteString("#   .TargetType        Rancher cluster type of the host\n")
	out.WriteString("#   .KubeconfigServer  reachable host address\n")
	out.WriteString("# Functions: add, sub, mul, mod, lower, upper\n")
	out.WriteString("#\n")
	out.WriteString("# Example: tlsSANs: [\"{{ .Name }}.k3k.example.com\"]\n")
	out.WriteString("\n")

	fmt.Fprintf(&out, "batch: %s\n", quoteYAML(options.Batch))
	fmt.Fprintf(&out, "count: %d\n", options.Count)
	fmt.Fprintf(&out, "parallelism: %d\n", options.Parallelism)
	fmt.Fprintf(&out, "perTargetParallelism: %d\n", options.PerTargetParallelism)
	fmt.Fprintf(&out, "createTimeout: %s\n", quoteYAML(options.CreateTimeout))
	fmt.Fprintf(&out, "artifactsDir: %s\n", quoteYAML(options.ArtifactsDir))
	out.WriteString("# lbAddressTimeout: 2m   # how long to wait for a LoadBalancer address before skipping it\n")
	out.WriteString("\n")

	out.WriteString("rancher:\n")
	fmt.Fprintf(&out, "  host: %s\n", quoteYAML(options.Rancher.Host))
	if options.Rancher.TokenEnv != "" {
		fmt.Fprintf(&out, "  tokenEnv: %s\n", quoteYAML(options.Rancher.TokenEnv))
	}
	fmt.Fprintf(&out, "  insecure: %t\n", options.Rancher.Insecure)
	if options.Rancher.CAFile != "" {
		fmt.Fprintf(&out, "  caFile: %s\n", quoteYAML(options.Rancher.CAFile))
	}
	fmt.Fprintf(&out, "  workspace: %s\n", quoteYAML(options.Rancher.Workspace))
	out.WriteString("  # Rancher treats a k3k virtual cluster as an imported K3s cluster. With version\n")
	out.WriteString("  # management enabled it deploys system-upgrade-controller and creates upgrade Plans\n")
	out.WriteString("  # for the cluster's nodes, which never complete because there are no real machines\n")
	out.WriteString("  # to upgrade, leaving the cluster stuck out of the active state.\n")
	out.WriteString("  # Set the K3s version with clusterSpec.version instead.\n")
	fmt.Fprintf(&out, "  versionManagement: %s   # false, true, or system-default\n",
		quoteYAML(options.Rancher.VersionManagement))
	out.WriteString("\n")

	if options.DiscoverTargets != nil {
		discovery := options.DiscoverTargets
		out.WriteString("# Select host clusters from Rancher by display-name prefix and cluster type.\n")
		out.WriteString("discoverTargets:\n")
		fmt.Fprintf(&out, "  prefix: %s\n", quoteYAML(discovery.Prefix))
		if discovery.Type != "" {
			fmt.Fprintf(&out, "  type: %s\n", quoteYAML(discovery.Type))
		}
		fmt.Fprintf(&out, "  address: %s\n", quoteYAML(discovery.Address))
		fmt.Fprintf(&out, "  allowInternalAddress: %t\n", discovery.AllowInternalAddress)
		fmt.Fprintf(&out, "  weight: %d\n", discovery.Weight)
		fmt.Fprintf(&out, "  parallelism: %d\n", discovery.Parallelism)
		out.WriteString("\n")
	} else if len(options.Targets) == 0 {
		out.WriteString("# Select host clusters from Rancher by display-name prefix and cluster type.\n")
		out.WriteString("# At least one of discoverTargets or targets is required.\n")
		out.WriteString("# discoverTargets:\n")
		out.WriteString("#   prefix: downstream-\n")
		out.WriteString("#   type: imported\n")
		out.WriteString("#   address: external\n")
		out.WriteString("#   allowInternalAddress: false\n")
		out.WriteString("#   weight: 1\n")
		out.WriteString("#   parallelism: 1\n")
		out.WriteString("\n")
	}

	if len(options.Targets) > 0 {
		out.WriteString("targets:\n")
		for _, target := range options.Targets {
			fmt.Fprintf(&out, "  - name: %s\n", quoteYAML(target.Name))
			if target.Type != "" {
				fmt.Fprintf(&out, "    type: %s\n", quoteYAML(target.Type))
			}
			fmt.Fprintf(&out, "    kubeconfig: %s\n", quoteYAML(target.Kubeconfig))
			fmt.Fprintf(&out, "    kubeconfigServer: %s\n", quoteYAML(target.KubeconfigServer))
			fmt.Fprintf(&out, "    weight: %d\n", target.Weight)
			fmt.Fprintf(&out, "    parallelism: %d\n", target.Parallelism)
		}
		out.WriteString("\n")
	} else {
		out.WriteString("# Explicit host clusters, which may be combined with discoverTargets.\n")
		out.WriteString("# targets:\n")
		out.WriteString("#   - name: downstream-0-0\n")
		out.WriteString("#     type: imported\n")
		out.WriteString("#     kubeconfig: /path/to/downstream-0-0.yaml\n")
		out.WriteString("#     kubeconfigServer: 192.0.2.10\n")
		out.WriteString("#     weight: 1\n")
		out.WriteString("#     parallelism: 1\n")
		out.WriteString("\n")
	}

	specBlock, err := renderSpecBlock(options.ClusterSpec)
	if err != nil {
		return nil, err
	}
	out.Write(specBlock)

	return out.Bytes(), nil
}

// renderSpecBlock emits the clusterSpec section, walking the ClusterSpec type so that every
// supported field is represented whether or not the user set it.
func renderSpecBlock(userSpec map[string]any) ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("clusterSpec:\n")

	if userSpec == nil {
		userSpec = map[string]any{}
	}

	fields, err := specFields(specStructType(), "")
	if err != nil {
		return nil, err
	}
	for _, field := range fields {
		block, err := renderField(field, userSpec, 1)
		if err != nil {
			return nil, err
		}
		out.Write(block)
	}
	return out.Bytes(), nil
}

// specField describes one field discovered by reflection over the spec types.
type specField struct {
	// Name is the YAML key.
	Name string
	// Path is the dotted lookup key into specDocs.
	Path string
	// Type is the underlying Go type, with pointers resolved.
	Type reflect.Type
	// Children are the nested fields of a struct-valued field.
	Children []specField
}

// maxSpecDepth bounds reflection so a field whose schema is documented by a curated example
// can never be expanded recursively by accident.
const maxSpecDepth = 4

// specStructType returns the reflected ClusterSpec type the generator walks.
func specStructType() reflect.Type { return reflect.TypeOf(ClusterSpec{}) }

func specFields(structType reflect.Type, prefix string) ([]specField, error) {
	if structType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("expected struct, got %s", structType.Kind())
	}

	var fields []specField
	for i := 0; i < structType.NumField(); i++ {
		structField := structType.Field(i)
		if !structField.IsExported() {
			continue
		}
		name := jsonFieldName(structField.Tag.Get("json"))
		if name == "" || name == "-" {
			continue
		}

		path := name
		if prefix != "" {
			path = prefix + "." + name
		}

		fieldType := structField.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		field := specField{Name: name, Path: path, Type: fieldType}

		// A curated example replaces recursion for types whose schema is impractical to expand.
		_, curated := specDocs[path]
		hasExample := curated && len(specDocs[path].Example) > 0
		depth := strings.Count(path, ".") + 1
		if fieldType.Kind() == reflect.Struct && !hasExample && depth < maxSpecDepth {
			children, err := specFields(fieldType, path)
			if err != nil {
				return nil, err
			}
			field.Children = children
		}

		fields = append(fields, field)
	}
	return fields, nil
}

func jsonFieldName(tag string) string {
	if tag == "" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// renderField emits one field, live when the user set it and commented otherwise.
func renderField(field specField, userSpec map[string]any, depth int) ([]byte, error) {
	indent := strings.Repeat("  ", depth)
	value, userSet := userSpec[field.Name]

	var out bytes.Buffer

	doc, documented := specDocs[field.Path]
	if documented {
		out.WriteString(commentBlock(doc, indent))
	}

	if userSet {
		// When the user set only part of a struct, emit the supplied keys live and keep the
		// remaining supported keys visible as commented placeholders.
		if nested, ok := value.(map[string]any); ok && len(field.Children) > 0 {
			fmt.Fprintf(&out, "%s%s:\n", indent, field.Name)
			for _, child := range field.Children {
				block, err := renderField(child, nested, depth+1)
				if err != nil {
					return nil, err
				}
				out.Write(block)
			}
			return out.Bytes(), nil
		}

		rendered, err := renderLiveValue(field.Name, value, indent)
		if err != nil {
			return nil, err
		}
		out.Write(rendered)
		out.WriteString("\n\n")
		return out.Bytes(), nil
	}

	if len(doc.Example) > 0 {
		for _, line := range doc.Example {
			fmt.Fprintf(&out, "%s# %s\n", indent, line)
		}
		out.WriteString("\n")
		return out.Bytes(), nil
	}

	if len(field.Children) > 0 {
		fmt.Fprintf(&out, "%s# %s:\n", indent, field.Name)
		for _, child := range field.Children {
			block, err := renderField(child, map[string]any{}, depth+1)
			if err != nil {
				return nil, err
			}
			out.Write(commentOut(block, indent))
		}
		return out.Bytes(), nil
	}

	fmt.Fprintf(&out, "%s# %s: %s\n", indent, field.Name, placeholderFor(field, doc))
	out.WriteString("\n")
	return out.Bytes(), nil
}

// commentOut prefixes any not-yet-commented, non-blank line with a comment marker, so nested
// placeholders under a commented parent stay commented.
func commentOut(block []byte, indent string) []byte {
	var out bytes.Buffer
	for _, line := range strings.Split(string(block), "\n") {
		if strings.TrimSpace(line) == "" {
			out.WriteString("\n")
			continue
		}
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "#") {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		fmt.Fprintf(&out, "%s# %s\n", indent, strings.TrimPrefix(line, indent))
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// commentBlock renders the description, default, and permitted values for a field.
func commentBlock(doc SpecDoc, indent string) string {
	var out bytes.Buffer
	for _, line := range wrapText(doc.Doc, 88) {
		fmt.Fprintf(&out, "%s# %s\n", indent, line)
	}
	if doc.Default != "" {
		fmt.Fprintf(&out, "%s# Default: %s\n", indent, doc.Default)
	}
	if len(doc.Enum) > 0 {
		fmt.Fprintf(&out, "%s# One of: %s\n", indent, strings.Join(doc.Enum, ", "))
	}
	if doc.Link != "" {
		fmt.Fprintf(&out, "%s# Schema: %s\n", indent, doc.Link)
	}
	return out.String()
}

// placeholderFor produces a syntactically valid sample value so that uncommenting a field
// yields a config that still parses.
func placeholderFor(field specField, doc SpecDoc) string {
	if doc.Placeholder != "" {
		return doc.Placeholder
	}
	if len(doc.Enum) > 0 {
		if doc.Default != "" {
			return doc.Default
		}
		return doc.Enum[0]
	}

	switch field.Type.Kind() {
	case reflect.String:
		if doc.Default != "" {
			return doc.Default
		}
		return `""`
	case reflect.Int, reflect.Int32, reflect.Int64:
		if doc.Default != "" {
			return doc.Default
		}
		return "0"
	case reflect.Bool:
		return "false"
	case reflect.Map:
		return "{}"
	case reflect.Slice:
		return "[]"
	case reflect.Struct:
		// resource.Quantity and other types with custom marshalling are scalars in YAML.
		if doc.Default != "" {
			return doc.Default
		}
		return `""`
	default:
		return `""`
	}
}

// renderLiveValue emits a user-supplied value as YAML under the given key.
func renderLiveValue(name string, value any, indent string) ([]byte, error) {
	encoded, err := sigsyaml.Marshal(map[string]any{name: value})
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}

	var out bytes.Buffer
	for _, line := range strings.Split(strings.TrimRight(string(encoded), "\n"), "\n") {
		fmt.Fprintf(&out, "%s%s\n", indent, line)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// quoteYAML renders a scalar as a single-quoted YAML string.
func quoteYAML(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// wrapText breaks a description into comment-width lines.
func wrapText(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	var lines []string
	var current strings.Builder
	for _, word := range strings.Fields(text) {
		if current.Len() > 0 && current.Len()+1+len(word) > width {
			lines = append(lines, current.String())
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteString(" ")
		}
		current.WriteString(word)
	}
	if current.Len() > 0 {
		lines = append(lines, current.String())
	}
	return lines
}

// LoadTargetsJSON reads a JSON array of targets from a file, or from stdin when path is "-".
// An empty path yields no targets.
func LoadTargetsJSON(path string) ([]Target, error) {
	if path == "" {
		return nil, nil
	}

	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read targets: %w", err)
	}

	// Target carries YAML tags rather than JSON tags, so decode through an explicit shape.
	var raw []struct {
		Name             string `json:"name"`
		Type             string `json:"type"`
		Kubeconfig       string `json:"kubeconfig"`
		KubeconfigServer string `json:"kubeconfigServer"`
		Weight           int    `json:"weight"`
		Parallelism      int    `json:"parallelism"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse targets: %w", err)
	}

	targets := make([]Target, 0, len(raw))
	for _, item := range raw {
		if item.Weight == 0 {
			item.Weight = 1
		}
		if item.Parallelism == 0 {
			item.Parallelism = 1
		}
		targets = append(targets, Target{
			Name: item.Name, Type: item.Type, Kubeconfig: item.Kubeconfig,
			KubeconfigServer: item.KubeconfigServer, Weight: item.Weight, Parallelism: item.Parallelism,
		})
	}
	return targets, nil
}

// LoadSpecValues merges a clusterSpec YAML file with dotted key=value overrides.
//
// The result is validated by decoding it into ClusterSpec, so a typo or a wrong type is
// reported when the config is generated rather than when a batch is first run.
func LoadSpecValues(path string, overrides []string) (map[string]any, error) {
	spec := map[string]any{}

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read spec file: %w", err)
		}
		if err := sigsyaml.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("parse spec file: %w", err)
		}
	}

	for _, override := range overrides {
		key, value, found := strings.Cut(override, "=")
		if !found {
			return nil, fmt.Errorf("invalid --set %q: expected key=value", override)
		}
		if err := setSpecPath(spec, strings.Split(key, "."), value); err != nil {
			return nil, err
		}
	}

	if len(spec) == 0 {
		return spec, nil
	}
	encoded, err := sigsyaml.Marshal(spec)
	if err != nil {
		return nil, err
	}
	if err := sigsyaml.UnmarshalStrict(encoded, &ClusterSpec{}); err != nil {
		return nil, fmt.Errorf("invalid clusterSpec: %w", err)
	}
	return spec, nil
}

// setSpecPath assigns a dotted key path, parsing the value as YAML so that numbers, booleans,
// and inline collections keep their intended type.
func setSpecPath(spec map[string]any, path []string, value string) error {
	current := spec
	for i, part := range path {
		if i == len(path)-1 {
			var parsed any
			if err := sigsyaml.Unmarshal([]byte(value), &parsed); err != nil {
				current[part] = value
				return nil
			}
			current[part] = parsed
			return nil
		}
		next, found := current[part]
		if !found {
			child := map[string]any{}
			current[part] = child
			current = child
			continue
		}
		child, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("cannot set %q: %q is not a mapping", strings.Join(path, "."), part)
		}
		current = child
	}
	return nil
}

// SortedSpecPaths returns every documented clusterSpec path, for tests and tooling.
func SortedSpecPaths() []string {
	paths := make([]string, 0, len(specDocs))
	for path := range specDocs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}
