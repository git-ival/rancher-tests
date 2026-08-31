package k3k

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Batch string `yaml:"batch"`
	Count int    `yaml:"count"`
	// Parallelism limits complete create or delete workflows across all targets.
	Parallelism int `yaml:"parallelism"`
	// PerTargetParallelism is the default limit for concurrent k3kcli operations against one host cluster.
	PerTargetParallelism int    `yaml:"perTargetParallelism"`
	Mode                 string `yaml:"mode"`
	Servers              int    `yaml:"servers"`
	Agents               int    `yaml:"agents"`
	PersistenceType      string `yaml:"persistenceType"`
	// CreateTimeout is parsed from CreateTimeoutValue and is not read directly from YAML.
	CreateTimeout      time.Duration `yaml:"-"`
	CreateTimeoutValue string        `yaml:"createTimeout"`
	ArtifactsDir       string        `yaml:"artifactsDir"`
	StateFile          string        `yaml:"stateFile"`
	// K3KCLI and Kubectl may be command names in PATH or explicit executable paths.
	K3KCLI  string        `yaml:"k3kcli"`
	Kubectl string        `yaml:"kubectl"`
	Rancher RancherConfig `yaml:"rancher"`
	// DiscoverTargets selects host clusters from Rancher and appends them to explicit Targets.
	DiscoverTargets *TargetDiscovery `yaml:"discoverTargets"`
	Targets         []Target         `yaml:"targets"`
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
}

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

	config := &Config{}
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
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
	if config.Rancher.Token == "" && config.Rancher.TokenEnv != "" {
		config.Rancher.Token = os.Getenv(config.Rancher.TokenEnv)
	}

	return config, config.Validate()
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
	if c.Rancher.Workspace == "" {
		c.Rancher.Workspace = "fleet-default"
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
	if len(c.Targets) == 0 && c.DiscoverTargets == nil {
		return errors.New("at least one target or discoverTargets is required")
	}
	if c.Mode != "shared" && c.Mode != "virtual" && c.Mode != "hcp" {
		return errors.New("mode must be shared, virtual, or hcp")
	}
	if c.PersistenceType != "dynamic" && c.PersistenceType != "ephemeral" {
		return errors.New("persistenceType must be dynamic or ephemeral")
	}
	if c.Servers < 1 || c.Agents < 0 {
		return errors.New("servers must be at least one and agents cannot be negative")
	}
	if c.Mode != "virtual" && c.Agents > 0 {
		return errors.New("agents are supported only in virtual mode")
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
