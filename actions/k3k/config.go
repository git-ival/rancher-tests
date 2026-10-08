package k3k

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	sigsyaml "sigs.k8s.io/yaml"
)

type Config struct {
	Batch string `yaml:"batch"`
	Count int    `yaml:"count"`
	// Parallelism limits complete create or delete workflows across all targets.
	Parallelism int `yaml:"parallelism"`
	// PerTargetParallelism is the default limit for concurrent k3kcli operations against one host cluster.
	PerTargetParallelism int `yaml:"perTargetParallelism"`
	// Mode, Servers, Agents, and PersistenceType are deprecated aliases for the equivalent
	// clusterSpec fields. They are folded into ClusterSpec when the corresponding clusterSpec
	// key is absent, and conflict with an explicit clusterSpec value.
	Mode            string `yaml:"mode"`
	Servers         int    `yaml:"servers"`
	Agents          int    `yaml:"agents"`
	PersistenceType string `yaml:"persistenceType"`
	// CreateTimeout is parsed from CreateTimeoutValue and is not read directly from YAML.
	CreateTimeout      time.Duration `yaml:"-"`
	CreateTimeoutValue string        `yaml:"createTimeout"`
	// LBAddressTimeout bounds LoadBalancer address discovery. It is parsed from
	// LBAddressTimeoutValue and is not read directly from YAML.
	LBAddressTimeout      time.Duration `yaml:"-"`
	LBAddressTimeoutValue string        `yaml:"lbAddressTimeout"`
	// RegisterTimeout bounds how long one cluster may take to register with Rancher and
	// report ready. It is parsed from RegisterTimeoutValue and is not read directly from YAML.
	RegisterTimeout      time.Duration `yaml:"-"`
	RegisterTimeoutValue string        `yaml:"registerTimeout"`
	ArtifactsDir         string        `yaml:"artifactsDir"`
	StateFile            string        `yaml:"stateFile"`
	// K3KCLI and Kubectl may be command names in PATH or explicit executable paths.
	K3KCLI  string        `yaml:"k3kcli"`
	Kubectl string        `yaml:"kubectl"`
	Rancher RancherConfig `yaml:"rancher"`
	// DiscoverTargets selects host clusters from Rancher and appends them to explicit Targets.
	DiscoverTargets *TargetDiscovery `yaml:"discoverTargets"`
	Targets         []Target         `yaml:"targets"`
	// ClusterSpec is the validated clusterSpec rendered with the sample context. Per-cluster
	// manifests re-render RawSpecYAML instead of reusing this value.
	ClusterSpec *ClusterSpec `yaml:"-"`
	// RawSpecYAML is the untemplated clusterSpec block as written by the user.
	RawSpecYAML []byte `yaml:"-"`
	// RawClusterSpec is the untemplated clusterSpec decoded as a plain map. It records exactly
	// which keys the user supplied, which drives alias folding and the config generator.
	RawClusterSpec map[string]any `yaml:"-"`
	// Warnings collects non-fatal configuration advisories raised during loading.
	Warnings []string `yaml:"-"`

	// rawTopLevel records which top-level keys the user supplied, so that deprecated aliases
	// can be distinguished from defaults when folding them into ClusterSpec.
	rawTopLevel map[string]any
}

type RancherConfig struct {
	Host string `yaml:"host"`
	// Token is the Rancher bearer token. TokenEnv is preferred to avoid storing it in the config file.
	Token    string `yaml:"token"`
	TokenEnv string `yaml:"tokenEnv"`
	Insecure bool   `yaml:"insecure"`
	CAFile   string `yaml:"caFile"`
	// Workspace is the Fleet namespace where imported provisioning Cluster resources are created.
	Workspace string `yaml:"workspace"`
	// VersionManagement controls Rancher's imported-cluster version management for the created
	// clusters: "false", "true", or "system-default".
	//
	// It defaults to "false". Rancher classifies a k3k virtual cluster as an imported K3s
	// cluster and, when version management is on, deploys system-upgrade-controller into it and
	// creates upgrade Plans for its nodes. A virtual cluster has no real machines to upgrade,
	// so the Plan stays in Applying, the Upgraded condition stays Unknown, and the cluster
	// never reaches active. The K3s version is set through clusterSpec.version instead.
	VersionManagement string `yaml:"versionManagement"`
}

// Rancher's cluster-level switch for imported-cluster version management. Annotations set on a
// provisioning cluster are copied onto the management cluster it creates.
const versionManagementAnnotation = "rancher.io/imported-cluster-version-management"

type Target struct {
	Name string `yaml:"name"`
	// Type is populated for discovered targets and is informational for explicit targets.
	Type       string `yaml:"type,omitempty"`
	Kubeconfig string `yaml:"kubeconfig"`
	// KubeconfigServer is a node or load-balancer address used in generated k3k kubeconfigs and TLS SANs.
	KubeconfigServer string `yaml:"kubeconfigServer"`
	// Weight controls the target's frequency in weighted round-robin cluster assignment.
	Weight int `yaml:"weight"`
	// Parallelism overrides the per-target limit for concurrent k3kcli operations.
	Parallelism int `yaml:"parallelism"`
}

type TargetDiscovery struct {
	Prefix string `yaml:"prefix"`
	// Type is the singular form of Types; both are combined when supplied.
	Type  string   `yaml:"type"`
	Types []string `yaml:"types"`
	// Address chooses external or internal Rancher node addresses for KubeconfigServer.
	Address string `yaml:"address"`
	// AllowInternalAddress permits fallback to an internal address when no external address exists.
	AllowInternalAddress bool `yaml:"allowInternalAddress"`
	Weight               int  `yaml:"weight"`
	Parallelism          int  `yaml:"parallelism"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// The clusterSpec block is removed before the rest of the file is parsed, because it is a
	// template and need not be valid YAML until it has been rendered.
	config := &Config{}
	outer, specBlock := splitClusterSpecBlock(data)
	if err := yaml.Unmarshal(outer, config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := config.extractClusterSpec(outer, specBlock); err != nil {
		return nil, err
	}
	config.setDefaults()
	if err := config.makePathsAbsolute(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if config.CreateTimeoutValue != "" {
		config.CreateTimeout, err = time.ParseDuration(config.CreateTimeoutValue)
		if err != nil {
			return nil, fmt.Errorf("parse createTimeout: %w", err)
		}
	}
	if config.LBAddressTimeoutValue != "" {
		config.LBAddressTimeout, err = time.ParseDuration(config.LBAddressTimeoutValue)
		if err != nil {
			return nil, fmt.Errorf("parse lbAddressTimeout: %w", err)
		}
	}
	if config.RegisterTimeoutValue != "" {
		config.RegisterTimeout, err = time.ParseDuration(config.RegisterTimeoutValue)
		if err != nil {
			return nil, fmt.Errorf("parse registerTimeout: %w", err)
		}
	}
	if config.Rancher.Token == "" && config.Rancher.TokenEnv != "" {
		config.Rancher.Token = os.Getenv(config.Rancher.TokenEnv)
	}

	return config, config.Validate()
}

// extractClusterSpec records the untemplated clusterSpec block and the top-level keys the user
// supplied.
//
// The block is kept as the exact text the user wrote, so that per-cluster renders are
// deterministic and the config generator can echo it back unchanged. RawClusterSpec is filled
// in later, once the block has been rendered and can be parsed.
func (c *Config) extractClusterSpec(outer, specBlock []byte) error {
	c.rawTopLevel = map[string]any{}
	if err := yaml.Unmarshal(outer, &c.rawTopLevel); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	c.RawClusterSpec = map[string]any{}
	c.RawSpecYAML = specBlock
	return nil
}

// splitClusterSpecBlock separates the clusterSpec block from the rest of the configuration.
//
// The split is lexical rather than a YAML parse because the block is a Go template: an
// expression such as "serverPort: {{ add 30000 .Index }}" is not valid YAML until it has been
// rendered, and YAML would otherwise read "{{ ... }}" as a flow mapping.
//
// The block is every line after a top-level "clusterSpec:" key up to the next line that starts
// in column zero. Removed lines are replaced by blank lines so that YAML error messages for
// the remaining document keep their original line numbers.
func splitClusterSpecBlock(data []byte) (outer, specBlock []byte) {
	lines := strings.Split(string(data), "\n")

	start := -1
	for index, line := range lines {
		if line == "clusterSpec:" || strings.HasPrefix(line, "clusterSpec:") &&
			!strings.HasPrefix(line, "clusterSpec::") && isTopLevelKey(line) {
			start = index
			break
		}
	}
	if start < 0 {
		return data, nil
	}

	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			continue
		}
		// A line that starts in column zero begins the next top-level key.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			end = index
			break
		}
	}

	var block []string
	// Preserve any value written inline after "clusterSpec:".
	if inline := strings.TrimSpace(strings.TrimPrefix(lines[start], "clusterSpec:")); inline != "" {
		block = append(block, inline)
	}
	block = append(block, lines[start+1:end]...)

	remaining := append([]string{}, lines[:start]...)
	for range lines[start:end] {
		remaining = append(remaining, "")
	}
	remaining = append(remaining, lines[end:]...)

	return []byte(strings.Join(remaining, "\n")), []byte(strings.Join(block, "\n"))
}

// isTopLevelKey reports whether a line declares a key in column zero.
func isTopLevelKey(line string) bool {
	return len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#'
}

func (c *Config) makePathsAbsolute(baseDir string) error {
	paths := []*string{&c.ArtifactsDir, &c.StateFile}
	if c.Rancher.CAFile != "" {
		paths = append(paths, &c.Rancher.CAFile)
	}
	for i := range c.Targets {
		paths = append(paths, &c.Targets[i].Kubeconfig)
	}
	for _, path := range paths {
		if !filepath.IsAbs(*path) {
			*path = filepath.Join(baseDir, *path)
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return fmt.Errorf("resolve path %q: %w", *path, err)
		}
		*path = absolute
	}
	return nil
}

func (c *Config) setDefaults() {
	if c.Parallelism == 0 {
		c.Parallelism = 4
	}
	if c.PerTargetParallelism == 0 {
		c.PerTargetParallelism = 2
	}
	if c.Mode == "" {
		c.Mode = "shared"
	}
	if c.Servers == 0 {
		c.Servers = 1
	}
	if c.PersistenceType == "" {
		c.PersistenceType = "ephemeral"
	}
	if c.CreateTimeoutValue == "" {
		c.CreateTimeoutValue = "10m"
		c.CreateTimeout = 10 * time.Minute
	}
	if c.LBAddressTimeoutValue == "" {
		c.LBAddressTimeoutValue = "2m"
		c.LBAddressTimeout = 2 * time.Minute
	}
	// Importing a cluster involves deploying cattle-cluster-agent and waiting for it to dial
	// home, which routinely takes minutes. This matches the 30m the rest of the repo allows.
	if c.RegisterTimeoutValue == "" {
		c.RegisterTimeoutValue = "30m"
		c.RegisterTimeout = 30 * time.Minute
	}
	if c.Rancher.Workspace == "" {
		c.Rancher.Workspace = "fleet-default"
	}
	if c.Rancher.VersionManagement == "" {
		c.Rancher.VersionManagement = "false"
	}
	if c.ArtifactsDir == "" {
		c.ArtifactsDir = "artifacts"
	}
	if c.StateFile == "" && c.Batch != "" {
		c.StateFile = c.ArtifactsDir + "/" + c.Batch + "/state.json"
	}
	if c.K3KCLI == "" {
		c.K3KCLI = "k3kcli"
	}
	if c.Kubectl == "" {
		c.Kubectl = "kubectl"
	}
	if c.DiscoverTargets != nil {
		if c.DiscoverTargets.Type != "" {
			c.DiscoverTargets.Types = append(c.DiscoverTargets.Types, c.DiscoverTargets.Type)
		}
		if c.DiscoverTargets.Address == "" {
			c.DiscoverTargets.Address = "external"
		}
		if c.DiscoverTargets.Weight == 0 {
			c.DiscoverTargets.Weight = 1
		}
		if c.DiscoverTargets.Parallelism == 0 {
			c.DiscoverTargets.Parallelism = c.PerTargetParallelism
		}
	}
	for i := range c.Targets {
		if c.Targets[i].Weight == 0 {
			c.Targets[i].Weight = 1
		}
		if c.Targets[i].Parallelism == 0 {
			c.Targets[i].Parallelism = c.PerTargetParallelism
		}
	}
}

func (c *Config) Validate() error {
	if c.Batch == "" {
		return errors.New("batch is required")
	}
	if !regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`).MatchString(c.Batch) || len(c.Batch) > 58 {
		return errors.New("batch must be a DNS label no longer than 58 characters")
	}
	if c.Count < 1 {
		return errors.New("count must be greater than zero")
	}
	if c.Parallelism < 1 || c.PerTargetParallelism < 1 {
		return errors.New("parallelism values must be greater than zero")
	}
	if c.Rancher.Host == "" || c.Rancher.Token == "" {
		return errors.New("rancher.host and rancher token are required")
	}
	switch c.Rancher.VersionManagement {
	case "false", "true", "system-default":
	default:
		return errors.New(`rancher.versionManagement must be "false", "true", or "system-default"`)
	}
	if len(c.Targets) == 0 && c.DiscoverTargets == nil {
		return errors.New("at least one target or discoverTargets is required")
	}
	if err := c.validateClusterSpec(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, target := range c.Targets {
		if target.Name == "" || target.Kubeconfig == "" || target.KubeconfigServer == "" {
			return errors.New("each target requires name, kubeconfig, and kubeconfigServer")
		}
		if target.Type != "" && !validTargetType(target.Type) {
			return fmt.Errorf("target %q has unsupported type %q", target.Name, target.Type)
		}
		if seen[target.Name] {
			return fmt.Errorf("duplicate target %q", target.Name)
		}
		seen[target.Name] = true
		if target.Weight < 1 || target.Parallelism < 1 {
			return fmt.Errorf("target %q weight and parallelism must be greater than zero", target.Name)
		}
	}
	if c.DiscoverTargets != nil {
		if c.DiscoverTargets.Address != "external" && c.DiscoverTargets.Address != "internal" {
			return errors.New("discoverTargets.address must be external or internal")
		}
		if c.DiscoverTargets.Weight < 1 || c.DiscoverTargets.Parallelism < 1 {
			return errors.New("discoverTargets weight and parallelism must be greater than zero")
		}
		for _, clusterType := range c.DiscoverTargets.Types {
			if !validTargetType(clusterType) {
				return fmt.Errorf("unsupported discovered target type %q", clusterType)
			}
		}
	}
	return nil
}

// validateClusterSpec renders the clusterSpec against a synthetic first-cluster context and
// validates the result, so template and schema errors surface at load time rather than
// mid-batch. The rendered spec is retained on the Config for inspection; per-cluster manifests
// re-render RawSpecYAML with their own context.
func (c *Config) validateClusterSpec() error {
	// The block is rendered before it is parsed, because template expressions need not be
	// valid YAML. The rendered form also yields the key set used for alias folding and by the
	// config generator.
	rendered, err := renderSpecYAML(c.RawSpecYAML, sampleSpecContext(c))
	if err != nil {
		return err
	}
	c.RawClusterSpec = map[string]any{}
	if len(bytes.TrimSpace(rendered)) > 0 {
		if err := sigsyaml.Unmarshal(rendered, &c.RawClusterSpec); err != nil {
			return fmt.Errorf("parse clusterSpec: %w", err)
		}
	}

	spec, err := decodeSpec(rendered)
	if err != nil {
		return err
	}
	aliasWarnings, err := c.applyDeprecatedAliases(spec)
	if err != nil {
		return err
	}
	if err := validateSpec(spec); err != nil {
		return err
	}
	if err := c.validatePerClusterUniqueness(spec); err != nil {
		return err
	}
	c.Warnings = append(c.Warnings, aliasWarnings...)
	c.Warnings = append(c.Warnings, specWarnings(spec)...)
	c.ClusterSpec = spec
	return nil
}

// ApplyDeprecatedAliases folds the deprecated top-level mode, servers, agents, and
// persistenceType fields into a rendered spec. It is exported for the per-cluster render path
// in manifest.go so that both paths produce identical specs.
//
// It does not record warnings, because it runs once per cluster from concurrent goroutines.
// Warnings are collected once during loading instead.
func (c *Config) ApplyDeprecatedAliases(spec *ClusterSpec) error {
	_, err := c.applyDeprecatedAliases(spec)
	return err
}

func (c *Config) applyDeprecatedAliases(spec *ClusterSpec) ([]string, error) {
	type alias struct {
		topKey   string
		specPath string
		set      func()
	}
	aliases := []alias{
		{"mode", "mode", func() { spec.Mode = c.Mode }},
		{"servers", "servers", func() { servers := int32(c.Servers); spec.Servers = &servers }},
		{"agents", "agents", func() { agents := int32(c.Agents); spec.Agents = &agents }},
		{"persistenceType", "persistence.type", func() {
			if spec.Persistence == nil {
				spec.Persistence = &PersistenceConfig{}
			}
			spec.Persistence.Type = c.PersistenceType
		}},
	}

	var warnings []string
	for _, item := range aliases {
		userSetAlias := c.rawTopLevel[item.topKey] != nil
		userSetSpec := rawSpecHasPath(c.RawClusterSpec, item.specPath)
		if userSetAlias && userSetSpec {
			return nil, fmt.Errorf("%q conflicts with clusterSpec.%s; set only one (prefer clusterSpec.%s)",
				item.topKey, item.specPath, item.specPath)
		}
		if !userSetSpec {
			item.set()
		}
		if userSetAlias {
			warnings = appendUnique(warnings, fmt.Sprintf(
				"%q is deprecated; use clusterSpec.%s instead", item.topKey, item.specPath))
		}
	}
	return warnings, nil
}

// rawSpecHasPath reports whether the user explicitly supplied a dotted key path.
func rawSpecHasPath(raw map[string]any, path string) bool {
	current := raw
	parts := strings.Split(path, ".")
	for i, part := range parts {
		value, found := current[part]
		if !found {
			return false
		}
		if i == len(parts)-1 {
			return true
		}
		next, ok := value.(map[string]any)
		if !ok {
			return false
		}
		current = next
	}
	return false
}

// validateSpec enforces the constraints k3k applies plus the ones k3kbulk adds because it
// renders manifests directly rather than going through k3kcli.
func validateSpec(spec *ClusterSpec) error {
	switch spec.Mode {
	case ModeShared, ModeVirtual, ModeHCP:
	default:
		return fmt.Errorf("clusterSpec.mode must be %s, %s, or %s", ModeShared, ModeVirtual, ModeHCP)
	}

	if spec.Servers == nil || *spec.Servers < 1 {
		return errors.New("clusterSpec.servers must be at least one")
	}
	agents := int32(0)
	if spec.Agents != nil {
		agents = *spec.Agents
	}
	if agents < 0 {
		return errors.New("clusterSpec.agents cannot be negative")
	}
	if agents > 0 && spec.Mode != ModeVirtual {
		return errors.New("clusterSpec.agents are supported only in virtual mode")
	}

	if spec.Persistence != nil {
		switch spec.Persistence.Type {
		case "", PersistenceDynamic, PersistenceEphemeral:
		default:
			return fmt.Errorf("clusterSpec.persistence.type must be %s or %s", PersistenceDynamic, PersistenceEphemeral)
		}
	}

	if spec.Expose != nil {
		exposed := 0
		for _, set := range []bool{spec.Expose.Ingress != nil, spec.Expose.LoadBalancer != nil, spec.Expose.NodePort != nil} {
			if set {
				exposed++
			}
		}
		if exposed > 1 {
			return errors.New("clusterSpec.expose.ingress, expose.loadBalancer, and expose.nodePort are mutually exclusive")
		}
		if spec.Expose.Ingress != nil && len(spec.TLSSANs) == 0 {
			return errors.New("clusterSpec.expose.ingress requires at least one clusterSpec.tlsSANs entry; the first entry becomes the Ingress host")
		}
	}

	// k3k discards ServerLimit when ServerResources is set, so accepting both would silently
	// drop configuration the user asked for.
	if spec.ServerLimit != nil && spec.ServerResources != nil {
		return errors.New("clusterSpec.serverLimit and clusterSpec.serverResources are mutually exclusive; serverResources overrides serverLimit")
	}
	if spec.WorkerLimit != nil && spec.WorkerResources != nil {
		return errors.New("clusterSpec.workerLimit and clusterSpec.workerResources are mutually exclusive; workerResources overrides workerLimit")
	}
	return nil
}

// specWarnings returns non-fatal advisories about a valid spec.
func specWarnings(spec *ClusterSpec) []string {
	var warnings []string
	if spec.ServerResources != nil || spec.WorkerResources != nil {
		warnings = append(warnings,
			"clusterSpec.serverResources/workerResources are applied by k3k as pod-level resources, "+
				"which requires the PodLevelResources feature gate on the host cluster")
	}
	return warnings
}

// validatePerClusterUniqueness rejects configurations where a single shared clusterSpec would
// make every cluster in the batch claim the same Ingress host or the same fixed node port.
func (c *Config) validatePerClusterUniqueness(spec *ClusterSpec) error {
	if c.Count < 2 {
		return nil
	}

	usesTemplate := bytes.Contains(c.RawSpecYAML, []byte("{{"))
	if spec.Expose != nil && spec.Expose.Ingress != nil && !usesTemplate {
		return fmt.Errorf("count is %d but clusterSpec.tlsSANs contains no template expression; "+
			"every cluster would create an Ingress for the same host. "+
			`Use a per-cluster value such as tlsSANs: ["{{ .Name }}.example.com"]`, c.Count)
	}
	if spec.Expose != nil && spec.Expose.NodePort != nil && !usesTemplate {
		if spec.Expose.NodePort.ServerPort != nil || spec.Expose.NodePort.ETCDPort != nil {
			return fmt.Errorf("count is %d but clusterSpec.expose.nodePort uses fixed ports; "+
				"clusters sharing a host would collide. "+
				"Use a per-cluster value such as serverPort: {{ add 30000 .Index }}", c.Count)
		}
	}
	if spec.Expose != nil && spec.Expose.LoadBalancer != nil && !usesTemplate {
		if spec.Expose.LoadBalancer.ServerPort != nil || spec.Expose.LoadBalancer.ETCDPort != nil {
			c.Warnings = appendUnique(c.Warnings, fmt.Sprintf(
				"count is %d with fixed clusterSpec.expose.loadBalancer ports; "+
					"this is only safe when each cluster receives a distinct load balancer address", c.Count))
		}
	}
	return nil
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func validTargetType(clusterType string) bool {
	switch clusterType {
	case "harvester", "imported", "hosted", "nodedriver", "custom", "local", "unknown", "aks", "eks", "gke", "ali":
		return true
	default:
		return false
	}
}

func (c *Config) ValidateResolvedTargets() error {
	if len(c.Targets) == 0 {
		return errors.New("target discovery matched no clusters")
	}
	return nil
}
