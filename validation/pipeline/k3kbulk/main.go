package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rancher/tests/actions/k3k"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "generate-config" {
		if err := generateConfig(os.Args[2:]); err != nil {
			fatal(err)
		}
		return
	}

	configPath := flag.String("config", "k3k-bulk.yaml", "path to the batch configuration")
	timeout := flag.Duration("timeout", 45*time.Minute, "overall operation timeout")
	flag.Parse()
	if flag.NArg() != 1 || (flag.Arg(0) != "create" && flag.Arg(0) != "delete") {
		fmt.Fprintln(os.Stderr, "usage: k3kbulk [flags] create|delete")
		fmt.Fprintln(os.Stderr, "       k3kbulk generate-config [flags]")
		os.Exit(2)
	}

	// Several lifecycle steps wait for minutes; without progress output they are
	// indistinguishable from a hang.
	k3k.SetProgressWriter(os.Stderr)

	config, err := k3k.LoadConfig(*configPath)
	if err != nil {
		fatal(err)
	}
	for _, warning := range config.Warnings {
		fmt.Fprintln(os.Stderr, "warning: "+warning)
	}
	rancherClient, err := k3k.NewRancherClient(config.Rancher)
	if err != nil {
		fatal(fmt.Errorf("create Rancher client: %w", err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := rancherClient.DiscoverTargets(ctx, config); err != nil {
		fatal(err)
	}
	state, err := k3k.LoadOrCreateState(config)
	if err != nil {
		fatal(err)
	}

	orchestrator := &k3k.Orchestrator{
		Config: config,
		State:  state,
		CLI: k3k.CLI{
			K3KCLI:  config.K3KCLI,
			Kubectl: config.Kubectl,
			Runner:  k3k.ExecRunner{},
		},
		Rancher: rancherClient,
	}
	if flag.Arg(0) == "create" {
		err = orchestrator.Create(ctx)
	} else {
		err = orchestrator.Delete(ctx)
	}
	if err != nil {
		fatal(err)
	}
}

// generateConfig writes an annotated configuration file in which every supported clusterSpec
// field is present, live when supplied and commented out otherwise.
func generateConfig(args []string) error {
	flags := flag.NewFlagSet("generate-config", flag.ExitOnError)
	output := flags.String("output", "config.yaml", "generated config path, or - for stdout")
	batch := flags.String("batch", "", "batch name (required)")
	count := flags.Int("count", 1, "number of k3k clusters to create")
	parallelism := flags.Int("parallelism", 4, "global workflow limit")
	perTarget := flags.Int("per-target-parallelism", 1, "per-host workflow limit")
	createTimeout := flags.String("create-timeout", "10m", "per-cluster readiness timeout")
	artifactsDir := flags.String("artifacts-dir", "artifacts", "artifacts directory")
	rancherHost := flags.String("rancher-host", "", "Rancher hostname (required)")
	tokenEnv := flags.String("rancher-token-env", "RANCHER_TOKEN", "env var holding the Rancher token")
	insecure := flags.Bool("rancher-insecure", false, "skip Rancher TLS verification")
	caFile := flags.String("rancher-ca", "", "Rancher HTTPS CA bundle")
	workspace := flags.String("rancher-workspace", "fleet-default", "Fleet workspace for imported clusters")
	versionManagement := flags.String("rancher-version-management", "false",
		"Rancher imported-cluster version management: false, true, or system-default")
	targetsJSON := flags.String("targets-json", "", "path to a JSON array of targets, or - for stdin")
	specFile := flags.String("spec-file", "", "path to a YAML file supplying clusterSpec values")

	discoverPrefix := flags.String("discover-prefix", "", "discover Rancher host clusters with this name prefix")
	discoverType := flags.String("discover-type", "", "discover Rancher host clusters of this type")
	discoverAddress := flags.String("discover-address", "external", "host node address to use: external or internal")
	discoverInternal := flags.Bool("discover-allow-internal-address", false, "fall back to an internal host address")
	discoverWeight := flags.Int("discover-weight", 1, "assignment weight for discovered targets")
	discoverParallelism := flags.Int("discover-parallelism", 1, "per-target limit for discovered targets")

	specFlags := newSpecFlags(flags)

	if err := flags.Parse(args); err != nil {
		return err
	}
	if *batch == "" {
		return fmt.Errorf("generate-config: --batch is required")
	}
	if *rancherHost == "" {
		return fmt.Errorf("generate-config: --rancher-host is required")
	}

	targets, err := k3k.LoadTargetsJSON(*targetsJSON)
	if err != nil {
		return err
	}
	spec, err := k3k.LoadSpecValues(*specFile, specFlags.values())
	if err != nil {
		return err
	}

	// Discovery is emitted only when asked for, so that a targets-only config stays clean.
	var discovery *k3k.TargetDiscovery
	if *discoverPrefix != "" || *discoverType != "" {
		discovery = &k3k.TargetDiscovery{
			Prefix:               *discoverPrefix,
			Type:                 *discoverType,
			Address:              *discoverAddress,
			AllowInternalAddress: *discoverInternal,
			Weight:               *discoverWeight,
			Parallelism:          *discoverParallelism,
		}
	}

	data, err := k3k.GenerateConfig(k3k.GenerateOptions{
		Batch:                *batch,
		Count:                *count,
		Parallelism:          *parallelism,
		PerTargetParallelism: *perTarget,
		CreateTimeout:        *createTimeout,
		ArtifactsDir:         *artifactsDir,
		Rancher: k3k.RancherConfig{
			Host:              *rancherHost,
			TokenEnv:          *tokenEnv,
			Insecure:          *insecure,
			CAFile:            *caFile,
			Workspace:         *workspace,
			VersionManagement: *versionManagement,
		},
		Targets:         targets,
		DiscoverTargets: discovery,
		ClusterSpec:     spec,
	})
	if err != nil {
		return err
	}

	if *output == "-" {
		_, err := os.Stdout.Write(data)
		return err
	}
	if err := os.WriteFile(*output, data, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote %s with %d target(s)\n", *output, len(targets))
	return nil
}

// specFlagSet collects the clusterSpec values that can be supplied directly on the command
// line. Anything not covered here is supplied through --spec-file.
type specFlagSet struct {
	mode               *string
	servers            *int
	agents             *int
	version            *string
	persistenceType    *string
	storageClassName   *string
	storageRequestSize *string
	set                *repeatedFlag
}

func newSpecFlags(flags *flag.FlagSet) *specFlagSet {
	set := &repeatedFlag{}
	flags.Var(set, "set", "clusterSpec value as key=value, repeatable (dotted keys supported)")
	return &specFlagSet{
		mode:               flags.String("mode", "", "clusterSpec.mode"),
		servers:            flags.Int("servers", 0, "clusterSpec.servers"),
		agents:             flags.Int("agents", 0, "clusterSpec.agents"),
		version:            flags.String("version", "", "clusterSpec.version"),
		persistenceType:    flags.String("persistence-type", "", "clusterSpec.persistence.type"),
		storageClassName:   flags.String("storage-class-name", "", "clusterSpec.persistence.storageClassName"),
		storageRequestSize: flags.String("storage-request-size", "", "clusterSpec.persistence.storageRequestSize"),
		set:                set,
	}
}

// values renders the flag set as dotted key/value pairs in a stable order.
func (s *specFlagSet) values() []string {
	var pairs []string
	add := func(key, value string) {
		if value != "" {
			pairs = append(pairs, key+"="+value)
		}
	}
	add("mode", *s.mode)
	if *s.servers > 0 {
		pairs = append(pairs, fmt.Sprintf("servers=%d", *s.servers))
	}
	if *s.agents > 0 {
		pairs = append(pairs, fmt.Sprintf("agents=%d", *s.agents))
	}
	add("version", *s.version)
	add("persistence.type", *s.persistenceType)
	add("persistence.storageClassName", *s.storageClassName)
	add("persistence.storageRequestSize", *s.storageRequestSize)
	return append(pairs, *s.set...)
}

// repeatedFlag collects a flag that may be supplied more than once.
type repeatedFlag []string

func (f *repeatedFlag) String() string { return "" }

func (f *repeatedFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
