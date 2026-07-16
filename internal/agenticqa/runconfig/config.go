// Package runconfig loads Agentic QA workflow configuration.
package runconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const Version = "v1"

// Config contains non-secret workflow settings.
type Config struct {
	Version     string            `yaml:"version"`
	Source      SourceConfig      `yaml:"source"`
	LLM         LLMConfig         `yaml:"llm"`
	Qase        QaseConfig        `yaml:"qase"`
	Jenkins     JenkinsConfig     `yaml:"jenkins"`
	Environment EnvironmentConfig `yaml:"environment"`
	Execution   ExecutionConfig   `yaml:"execution"`
	Artifacts   ArtifactConfig    `yaml:"artifacts"`
	Paths       PathsConfig       `yaml:"paths"`
}

type SourceConfig struct {
	Repo string `yaml:"repo"`
	PR   int    `yaml:"pr"`
}

type LLMConfig struct {
	Provider       string `yaml:"provider"`
	VertexProject  string `yaml:"vertexProject"`
	VertexLocation string `yaml:"vertexLocation"`
	SonnetModel    string `yaml:"sonnetModel"`
	HaikuModel     string `yaml:"haikuModel"`
}

type QaseConfig struct {
	Project string `yaml:"project"`
	MCPURL  string `yaml:"mcpUrl"`
}

type JenkinsConfig struct {
	URL  string `yaml:"url"`
	User string `yaml:"user"`
}

type EnvironmentConfig struct {
	SizingProfile     string `yaml:"sizingProfile"`
	StaticOnly        bool   `yaml:"staticOnly"`
	RecommendSpecs    bool   `yaml:"recommendSpecs"`
	ResolveVersions   bool   `yaml:"resolveVersions"`
	IncludePrerelease bool   `yaml:"includePrereleases"`
}

type ExecutionConfig struct {
	TestTimeout     string `yaml:"testTimeout"`
	MaxReruns       *int   `yaml:"maxReruns"`
	DryRun          bool   `yaml:"dryRun"`
	LocalTest       bool   `yaml:"localTest"`
	CleanupAfterRun bool   `yaml:"cleanupAfterRun"`
}

type ArtifactConfig struct {
	Backend        string `yaml:"backend"`
	Bucket         string `yaml:"bucket"`
	Prefix         string `yaml:"prefix"`
	Region         string `yaml:"region"`
	URLTTL         string `yaml:"urlTTL"`
	Endpoint       string `yaml:"endpoint"`
	ForcePathStyle bool   `yaml:"forcePathStyle"`
}

type PathsConfig struct {
	Workspace string      `yaml:"workspace"`
	Inputs    InputPaths  `yaml:"inputs"`
	Outputs   OutputPaths `yaml:"outputs"`
}

type InputPaths struct {
	PipelineEnv       string `yaml:"pipelineEnv"`
	FeatureMapping    string `yaml:"featureMapping"`
	TriggerMapping    string `yaml:"triggerMapping"`
	TriageFramework   string `yaml:"triageFramework"`
	AdditionalContext string `yaml:"additionalContext"`
	TestRepoRoot      string `yaml:"testRepoRoot"`
	ChartsDir         string `yaml:"chartsDir"`
	JJBDir            string `yaml:"jjbDir"`
	ValidationDir     string `yaml:"validationDir"`
	ActionsDir        string `yaml:"actionsDir"`
}

type OutputPaths struct {
	State                string `yaml:"state"`
	PipelineEnv          string `yaml:"pipelineEnv"`
	FeatureMapping       string `yaml:"featureMapping"`
	TriggerMapping       string `yaml:"triggerMapping"`
	IdentifiedTests      string `yaml:"identifiedTests"`
	EnvironmentPlan      string `yaml:"environmentPlan"`
	EnvironmentArtifacts string `yaml:"environmentArtifacts"`
	SetupEnvironments    string `yaml:"setupEnvironments"`
	TriggeredJobs        string `yaml:"triggeredJobs"`
	CompletedJobs        string `yaml:"completedJobs"`
	TriageResults        string `yaml:"triageResults"`
	DefectActions        string `yaml:"defectActions"`
	ConfigActions        string `yaml:"configActions"`
	CleanupResult        string `yaml:"cleanupResult"`
	Summary              string `yaml:"summary"`
}

// Load parses config and resolves paths against the config directory.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening run config: %w", err)
	}
	defer f.Close()

	var cfg Config
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing run config: %w", err)
	}
	if cfg.Version != Version {
		return nil, fmt.Errorf("unsupported run config version %q; expected %q", cfg.Version, Version)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolving run config path: %w", err)
	}
	cfg.resolvePaths(filepath.Dir(abs))
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate checks cross-field workflow requirements.
func (c *Config) Validate() error {
	if c.Paths.Outputs.State == "" {
		return fmt.Errorf("paths.outputs.state is required")
	}
	if c.Artifacts.Backend == "s3" {
		if c.Artifacts.Bucket == "" {
			return fmt.Errorf("artifacts.bucket is required for s3")
		}
		if c.Artifacts.URLTTL == "" {
			return fmt.Errorf("artifacts.urlTTL is required for s3")
		}
	}
	return nil
}

func (c *Config) resolvePaths(base string) {
	resolve := func(path string) string {
		if path == "" || path == "-" || filepath.IsAbs(path) {
			return path
		}
		return filepath.Clean(filepath.Join(base, path))
	}

	p := &c.Paths
	p.Workspace = resolve(p.Workspace)
	p.Inputs.PipelineEnv = resolve(p.Inputs.PipelineEnv)
	p.Inputs.FeatureMapping = resolve(p.Inputs.FeatureMapping)
	p.Inputs.TriggerMapping = resolve(p.Inputs.TriggerMapping)
	p.Inputs.TriageFramework = resolve(p.Inputs.TriageFramework)
	p.Inputs.AdditionalContext = resolve(p.Inputs.AdditionalContext)
	p.Inputs.TestRepoRoot = resolve(p.Inputs.TestRepoRoot)
	p.Inputs.ChartsDir = resolve(p.Inputs.ChartsDir)
	p.Inputs.JJBDir = resolve(p.Inputs.JJBDir)
	p.Inputs.ValidationDir = resolve(p.Inputs.ValidationDir)
	p.Inputs.ActionsDir = resolve(p.Inputs.ActionsDir)
	p.Outputs.State = resolve(p.Outputs.State)
	p.Outputs.PipelineEnv = resolve(p.Outputs.PipelineEnv)
	p.Outputs.FeatureMapping = resolve(p.Outputs.FeatureMapping)
	p.Outputs.TriggerMapping = resolve(p.Outputs.TriggerMapping)
	p.Outputs.IdentifiedTests = resolve(p.Outputs.IdentifiedTests)
	p.Outputs.EnvironmentPlan = resolve(p.Outputs.EnvironmentPlan)
	p.Outputs.EnvironmentArtifacts = resolve(p.Outputs.EnvironmentArtifacts)
	p.Outputs.SetupEnvironments = resolve(p.Outputs.SetupEnvironments)
	p.Outputs.TriggeredJobs = resolve(p.Outputs.TriggeredJobs)
	p.Outputs.CompletedJobs = resolve(p.Outputs.CompletedJobs)
	p.Outputs.TriageResults = resolve(p.Outputs.TriageResults)
	p.Outputs.DefectActions = resolve(p.Outputs.DefectActions)
	p.Outputs.ConfigActions = resolve(p.Outputs.ConfigActions)
	p.Outputs.CleanupResult = resolve(p.Outputs.CleanupResult)
	p.Outputs.Summary = resolve(p.Outputs.Summary)
}
