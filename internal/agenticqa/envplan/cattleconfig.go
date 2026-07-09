package envplan

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	// giBToMiB converts GiB to MiB; vSphere memorySize and diskSize fields
	// expect MiB rather than GiB.
	giBToMiB = 1024

	// cattle-config YAML key names used in output-generation logic.
	yamlKeyMachinePoolConfig = "machinePoolConfig"
	yamlKeyQuantity          = "quantity"
	yamlKeyEC2ConfigList     = "awsEC2Config"
	yamlKeySecurityGroup     = "securityGroup"
	yamlKeyAWSSecurityGroups = "awsSecurityGroups"
	yamlKeyRoles             = "roles"
	yamlKeyVolumeSize        = "volumeSize"
	yamlKeyPort              = "port"

	// awsEC2Configs outer YAML key (hardcoded for AWS; other providers use
	// envconfig.MachineListKeyForProvider).
	yamlKeyAWSEC2Configs = "awsEC2Configs"
)

// Field-rendering classification. Most cattle-config values are strings (and
// ${VAR} placeholders are fine as quoted strings — envsubst replaces the inner
// text). A few fields are YAML sequences or integers and must be rendered
// accordingly so the post-envsubst document is schema-valid.
var (
	// listFields are rendered as YAML sequences. A placeholder value such as
	// "${AWS_SECURITY_GROUP_NAMES}" becomes a single-element list; expand to a
	// comma-or-space-separated env value at runtime if multiple are needed.
	listFields = map[string]bool{
		yamlKeySecurityGroup:     true,
		yamlKeyAWSSecurityGroups: true,
		yamlKeyRoles:             true,
	}
	// rawScalarFields are emitted as unquoted scalars so that an integer-typed
	// destination field (e.g. awsEC2Configs.volumeSize int) parses correctly
	// after envsubst. Quoting would make it a string and fail unmarshalling.
	rawScalarFields = map[string]bool{
		yamlKeyVolumeSize: true,
		yamlKeyPort:       true,
	}
)

// GenerateCattleConfig renders a complete, ready-to-envsubst cattle-config.yaml
// for one environment group. It combines:
//   - the test-derived sections (provisioningInput / clusterConfig) computed by
//     plan-environment, and
//   - the organisation's non-test sections (rancher, cloud credentials,
//     provider machine configs, awsEC2Configs, registryInput, sshPath) taken
//     verbatim from the CattleConfigTemplate.
//
// Sensitive values are ${VAR} placeholders supplied by the template; shepherd
// does NOT expand env vars, so the pipeline must run `envsubst` (or equivalent)
// on this file before pointing CATTLE_TEST_CONFIG at it.
//
// The provider is taken from the group's derived cluster requirement, falling
// back to the template's DefaultProvider. Only that provider's credential and
// machine-config blocks are emitted.
// preferredFamilies is the ordered instance-family preference from the active
// sizing profile (e.g. ["t3a","t3"]); it steers AWS instance-type selection
// toward cheaper families. Pass nil for no preference (cheapest-overall).
func GenerateCattleConfig(g types.EnvironmentGroup, tmpl envconfig.CattleConfigTemplate, preferredFamilies []string) ([]byte, error) {
	cluster := g.Cluster
	root := yamlMap()

	provider := cluster.Provider
	if provider == "" {
		provider = tmpl.DefaultProvider
	}
	nodeProvider := cluster.NodeProvider
	if nodeProvider == "" {
		nodeProvider = tmpl.DefaultNodeProviderByProvider[provider]
	}

	// Resolve the effective Kubernetes version: an explicit pin from the test,
	// otherwise the distro's env-var placeholder so envsubst fills it in.
	k8sVersion := cluster.KubernetesVersion
	if k8sVersion == "" {
		k8sVersion = tmpl.KubernetesVersionEnvByDistro[cluster.KubernetesDistro]
	}

	// --- rancher ---
	if len(tmpl.Rancher) > 0 {
		addChild(root, "rancher", stringMapNode(tmpl.Rancher))
	}

	// --- registryInput ---
	if len(tmpl.RegistryInput) > 0 {
		addChild(root, "registryInput", stringMapNode(tmpl.RegistryInput))
	}

	// --- cloud credentials (provider-specific) ---
	if creds, ok := tmpl.Credentials[provider]; ok && len(creds) > 0 {
		if key := envconfig.CredentialKeyForProvider(provider); key != "" {
			addChild(root, key, stringMapNode(creds))
		}
	}

	// --- provisioningInput ---
	provIn := buildProvisioningInput(cluster, provider, nodeProvider, k8sVersion)
	if provIn != nil && len(provIn.Content) > 0 {
		addChild(root, "provisioningInput", provIn)
	}

	// --- clusterConfig ---
	clusterCfg := buildClusterConfig(cluster, provider, nodeProvider, k8sVersion, tmpl)
	if clusterCfg != nil && len(clusterCfg.Content) > 0 {
		addChild(root, "clusterConfig", clusterCfg)
	}

	// --- provider machine config (e.g. awsMachineConfigs) ---
	// One machine-config entry is emitted per node pool, each carrying that
	// pool's specific roles and recommended instance type. This lets the
	// Rancher provisioner match the right machine config to each pool via
	// MatchNodeRolesToMachinePool, and prevents a single oversized instance
	// type from being applied to all roles (e.g. etcd nodes don't need the
	// same sizing as worker nodes).
	if mc, ok := tmpl.MachineConfigs[provider]; ok {
		key := envconfig.MachineConfigsKeyForProvider(provider)
		listKey := envconfig.MachineListKeyForProvider(provider)
		if key != "" && listKey != "" {
			addChild(root, key, machineConfigNodePerPool(mc, listKey, cluster.NodePools, provider, tmpl, preferredFamilies))
		}
	}

	// --- awsEC2Configs (custom/ec2 provisioning), aws only ---
	// awsEC2Configs is a provisioner-level block (not per-pool), so we use the
	// spec from the most resource-demanding pool (workers/all-roles) rather
	// than the element-wise max across all pools.
	if tmpl.EC2Config != nil && provider == types.ProviderAWS {
		workerSpec := workerPoolSpec(cluster.NodePools)
		overrides := ec2SpecOverrides(workerSpec)
		allRoles := unionRoles(cluster.NodePools)
		addChild(root, "awsEC2Configs", ec2ConfigNode(*tmpl.EC2Config, allRoles, overrides))
	}

	// --- sshPath ---
	if tmpl.SSHPath != "" {
		ssh := yamlMap()
		addChild(ssh, "sshPath", scalarNode(tmpl.SSHPath, "sshPath"))
		addChild(root, "sshPath", ssh)
	}

	header := fmt.Sprintf(""+
		"# Generated by agentic-qa plan-environment.\n"+
		"# Group: %s\n"+
		"# Provider: %s   Distro: %s   Total downstream nodes: %d\n"+
		"#\n"+
		"# This is a COMPLETE cattle-config. Sensitive values are ${VAR}\n"+
		"# placeholders: run `envsubst < this-file > expanded.yaml` (or set them\n"+
		"# in the environment and template before use) and point\n"+
		"# CATTLE_TEST_CONFIG at the expanded file. shepherd does not expand env\n"+
		"# vars itself.\n",
		g.Name, emptyDash(provider), emptyDash(cluster.KubernetesDistro), cluster.TotalNodes)

	var buf bytes.Buffer
	buf.WriteString(header)
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		_ = enc.Close()
		return nil, fmt.Errorf("encoding cattle-config yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("closing yaml encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// buildProvisioningInput assembles the provisioningInput mapping node.
func buildProvisioningInput(cluster types.ClusterRequirement, provider, nodeProvider, k8sVersion string) *yaml.Node {
	n := yamlMap()
	if pools := machinePoolsNode(cluster.NodePools); pools != nil {
		addChild(n, "machinePools", pools)
	}
	if k8sVersion != "" {
		switch cluster.KubernetesDistro {
		case types.DistroK3S:
			addChild(n, "k3sKubernetesVersion", seqNode([]string{k8sVersion}, ""))
		case types.DistroRKE2, "":
			addChild(n, "rke2KubernetesVersion", seqNode([]string{k8sVersion}, ""))
		}
	}
	if cluster.CNI != "" {
		addChild(n, "cni", seqNode([]string{cluster.CNI}, ""))
	}
	if provider != "" {
		addChild(n, "providers", seqNode([]string{provider}, ""))
	}
	if nodeProvider != "" {
		addChild(n, "nodeProviders", seqNode([]string{nodeProvider}, ""))
	}
	if cluster.PSACT != "" {
		addChild(n, "psact", scalarNode(cluster.PSACT, "psact"))
	}
	if cluster.Hardened {
		addChild(n, "hardened", boolNode(true))
	}
	return n
}

// buildClusterConfig assembles the clusterConfig mapping node.
func buildClusterConfig(cluster types.ClusterRequirement, provider, nodeProvider, k8sVersion string, tmpl envconfig.CattleConfigTemplate) *yaml.Node {
	n := yamlMap()
	if pools := machinePoolsNode(cluster.NodePools); pools != nil {
		addChild(n, "machinePools", pools)
	}
	if k8sVersion != "" {
		addChild(n, "kubernetesVersion", scalarNode(k8sVersion, "kubernetesVersion"))
	}
	if cluster.CNI != "" {
		addChild(n, "cni", scalarNode(cluster.CNI, "cni"))
	}
	if provider != "" {
		addChild(n, "provider", scalarNode(provider, "provider"))
	}
	if nodeProvider != "" {
		addChild(n, "nodeProvider", scalarNode(nodeProvider, "nodeProvider"))
	}
	if cluster.PSACT != "" {
		addChild(n, "psact", scalarNode(cluster.PSACT, "psact"))
	}
	if net := networkingNode(cluster.Networking); net != nil {
		addChild(n, "networking", net)
	}
	if len(tmpl.ClusterRegistries) > 0 {
		addChild(n, "registries", anyNode(tmpl.ClusterRegistries))
	}
	addChild(n, "hardened", boolNode(cluster.Hardened))
	return n
}

// machinePoolsNode renders the machinePools list (shared by provisioningInput
// and clusterConfig).
func machinePoolsNode(pools []types.NodeRequirement) *yaml.Node {
	if len(pools) == 0 {
		return nil
	}
	seq := yamlSeq()
	for _, p := range pools {
		cfg := yamlMap()
		if p.Etcd {
			addChild(cfg, types.RoleEtcd, boolNode(true))
		}
		if p.ControlPlane {
			addChild(cfg, types.RoleControlPlane, boolNode(true))
		}
		if p.Worker {
			addChild(cfg, types.RoleWorker, boolNode(true))
		}
		if p.Windows {
			addChild(cfg, types.RoleWindows, boolNode(true))
		}
		qty := p.Quantity
		if qty < 1 {
			qty = 1
		}
		addChild(cfg, yamlKeyQuantity, intNode(qty))

		entry := yamlMap()
		addChild(entry, yamlKeyMachinePoolConfig, cfg)
		seq.Content = append(seq.Content, entry)
	}
	return seq
}

func networkingNode(net *types.NetworkingRequirement) *yaml.Node {
	if net == nil {
		return nil
	}
	n := yamlMap()
	wrote := false
	if net.LocalClusterAuthEndpoint {
		ace := yamlMap()
		addChild(ace, "enabled", boolNode(true))
		addChild(n, "localClusterAuthEndpoint", ace)
		wrote = true
	}
	if net.StackPreference != "" {
		addChild(n, "stackPreference", scalarNode(net.StackPreference, "stackPreference"))
		wrote = true
	}
	if net.ClusterCIDR != "" {
		addChild(n, "clusterCIDR", scalarNode(net.ClusterCIDR, "clusterCIDR"))
		wrote = true
	}
	if net.ServiceCIDR != "" {
		addChild(n, "serviceCIDR", scalarNode(net.ServiceCIDR, "serviceCIDR"))
		wrote = true
	}
	if !wrote {
		return nil
	}
	return n
}

// machineConfigNodePerPool renders a provider machineConfigs block with one
// inner machine-config list entry per node pool. Each entry carries the pool's
// own role list and its per-pool recommended spec (instance type, disk), so the
// Rancher provisioner can match the correct machine config to each pool via its
// MatchNodeRolesToMachinePool logic.
//
// When no pool carries a Spec (--recommend-specs was not used), all entries use
// the template's placeholder fields verbatim.
func machineConfigNodePerPool(
	mc envconfig.ProviderMachineConfig,
	listKey string,
	pools []types.NodeRequirement,
	provider string,
	tmpl envconfig.CattleConfigTemplate,
	preferredFamilies []string,
) *yaml.Node {
	n := yamlMap()
	for _, k := range sortedKeysStr(mc.OuterFields) {
		addChild(n, k, scalarNode(mc.OuterFields[k], k))
	}

	seq := yamlSeq()
	for _, pool := range pools {
		roles := poolRoles(pool)
		overrides := machineSpecOverrides(provider, pool.Spec, tmpl, preferredFamilies)
		merged := mergeFields(mc.MachineEntry, overrides)

		entry := yamlMap()
		if len(roles) > 0 {
			addChild(entry, "roles", seqNode(roles, "roles"))
		}
		for _, k := range sortedKeysStr(merged) {
			addChild(entry, k, fieldNode(k, merged[k]))
		}
		seq.Content = append(seq.Content, entry)
	}
	addChild(n, listKey, seq)
	return n
}

// poolRoles returns the role name list for a single node pool, used when
// constructing per-pool machine-config entries.
func poolRoles(p types.NodeRequirement) []string {
	var roles []string
	if p.Etcd {
		roles = append(roles, types.RoleEtcd)
	}
	if p.ControlPlane {
		roles = append(roles, types.RoleControlPlane)
	}
	if p.Worker {
		roles = append(roles, types.RoleWorker)
	}
	if p.Windows {
		roles = append(roles, types.RoleWindows)
	}
	if len(roles) == 0 {
		roles = []string{types.RoleEtcd, types.RoleControlPlane, types.RoleWorker}
	}
	return roles
}

// workerPoolSpec returns the Spec from the pool bearing worker or all-roles
// responsibility (the pool that actually runs test workloads), used for the
// awsEC2Configs provisioner block which is not per-pool. Falls back to
// effectiveSpec when no worker pool is found.
func workerPoolSpec(pools []types.NodeRequirement) *types.MachineSpec {
	for i := range pools {
		if pools[i].Worker {
			return pools[i].Spec
		}
	}
	return effectiveSpec(pools)
}

func ec2ConfigNode(ec2 envconfig.EC2ConfigTemplate, roles []string, overrides map[string]string) *yaml.Node {
	n := yamlMap()
	for _, k := range sortedKeysStr(ec2.OuterFields) {
		addChild(n, k, scalarNode(ec2.OuterFields[k], k))
	}
	entry := yamlMap()
	merged := mergeFields(ec2.Entry, overrides)
	for _, k := range sortedKeysStr(merged) {
		addChild(entry, k, fieldNode(k, merged[k]))
	}
	if len(roles) > 0 {
		addChild(entry, "roles", seqNode(roles, "roles"))
	}
	seq := yamlSeq()
	seq.Content = append(seq.Content, entry)
	addChild(n, yamlKeyEC2ConfigList, seq)
	return n
}

// mergeFields returns base with overrides applied (overrides win). Neither
// input is mutated. nil overrides returns base unchanged.
func mergeFields(base, overrides map[string]string) map[string]string {
	if len(overrides) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

// effectiveSpec returns the element-wise max MachineSpec across all pools that
// carry one, or nil when none do (i.e. --recommend-specs was not used).
func effectiveSpec(pools []types.NodeRequirement) *types.MachineSpec {
	var eff *types.MachineSpec
	for i := range pools {
		eff = MaxSpec(eff, pools[i].Spec)
	}
	return eff
}

// machineSpecOverrides maps an abstract spec to provider machine-config fields.
//   - AWS: instanceType (selected from the catalog) + rootSize (disk).
//   - Harvester/vSphere: cpuCount + memorySize + diskSize (raw values).
//
// Returns nil when spec is nil so the template placeholders are kept verbatim.
func machineSpecOverrides(provider string, spec *types.MachineSpec, tmpl envconfig.CattleConfigTemplate, preferredFamilies []string) map[string]string {
	if spec == nil {
		return nil
	}
	out := map[string]string{}
	switch strings.ToLower(provider) {
	case types.ProviderAWS:
		if it, ok := tmpl.SelectInstanceTypeForSpec(provider, spec.VCPUs, spec.MemoryGiB, preferredFamilies); ok {
			out["instanceType"] = it.Name
			spec.InstanceType = it.Name
		}
		out["rootSize"] = fmt.Sprintf("%d", spec.DiskGiB)
	case types.ProviderHarvester:
		out["cpuCount"] = fmt.Sprintf("%d", spec.VCPUs)
		out["memorySize"] = fmt.Sprintf("%d", spec.MemoryGiB)
		out["diskSize"] = fmt.Sprintf("%d", spec.DiskGiB)
	case types.ProviderVsphere, types.ProviderVsphereCloud:
		out["cpuCount"] = fmt.Sprintf("%d", spec.VCPUs)
		out["memorySize"] = fmt.Sprintf("%d", spec.MemoryGiB*giBToMiB)
		out["diskSize"] = fmt.Sprintf("%d", spec.DiskGiB*giBToMiB)
	default:
		// Unknown provider: try instanceType selection, else nothing.
		if it, ok := tmpl.SelectInstanceTypeForSpec(provider, spec.VCPUs, spec.MemoryGiB, preferredFamilies); ok {
			out["instanceType"] = it.Name
			spec.InstanceType = it.Name
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ec2SpecOverrides maps the spec onto awsEC2Configs fields (instanceType +
// volumeSize). Returns nil when spec is nil.
func ec2SpecOverrides(spec *types.MachineSpec) map[string]string {
	if spec == nil {
		return nil
	}
	out := map[string]string{
		"volumeSize": fmt.Sprintf("%d", spec.DiskGiB),
	}
	if spec.InstanceType != "" {
		out["instanceType"] = spec.InstanceType
	}
	return out
}

// unionRoles returns the union of all node-pool roles in the group, used to
// populate machine-config "roles" lists.
func unionRoles(pools []types.NodeRequirement) []string {
	var etcd, cp, worker, windows bool
	for _, p := range pools {
		etcd = etcd || p.Etcd
		cp = cp || p.ControlPlane
		worker = worker || p.Worker
		windows = windows || p.Windows
	}
	var roles []string
	if etcd {
		roles = append(roles, types.RoleEtcd)
	}
	if cp {
		roles = append(roles, types.RoleControlPlane)
	}
	if worker {
		roles = append(roles, types.RoleWorker)
	}
	if windows {
		roles = append(roles, types.RoleWindows)
	}
	if len(roles) == 0 {
		// Default to all roles so a single-node config is still valid.
		roles = []string{types.RoleEtcd, types.RoleControlPlane, types.RoleWorker}
	}
	return roles
}

// fieldNode renders a value with the correct YAML kind for the given field name
// (sequence for list fields, raw unquoted scalar for int-typed fields, else a
// plain scalar).
func fieldNode(field, value string) *yaml.Node {
	if listFields[field] {
		return seqNode([]string{value}, field)
	}
	if rawScalarFields[field] {
		return rawScalar(value)
	}
	return scalarNode(value, field)
}

// ---- yaml.Node helpers ---------------------------------------------------

func yamlMap() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"} }
func yamlSeq() *yaml.Node { return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"} }

func addChild(m *yaml.Node, key string, val *yaml.Node) {
	if val == nil {
		return
	}
	m.Content = append(m.Content, scalarNode(key, key), val)
}

// scalarNode emits a normal (quoted-if-needed) string scalar.
func scalarNode(value, _ string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

// rawScalar emits an unquoted scalar (no explicit tag) so that values like
// "${AWS_ROOT_SIZE}" remain unquoted and parse as the destination type after
// envsubst.
func rawScalar(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

func boolNode(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

func intNode(i int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprintf("%d", i)}
}

func seqNode(values []string, field string) *yaml.Node {
	seq := yamlSeq()
	for _, v := range values {
		if field != "" && rawScalarFields[field] {
			seq.Content = append(seq.Content, rawScalar(v))
		} else {
			seq.Content = append(seq.Content, scalarNode(v, field))
		}
	}
	return seq
}

func stringMapNode(m map[string]string) *yaml.Node {
	n := yamlMap()
	for _, k := range sortedKeysStr(m) {
		addChild(n, k, fieldNode(k, m[k]))
	}
	return n
}

// anyNode converts an arbitrary map[string]any / []any / scalar into a
// yaml.Node tree, used for the free-form clusterConfig.registries block.
func anyNode(v any) *yaml.Node {
	switch t := v.(type) {
	case map[string]any:
		n := yamlMap()
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			n.Content = append(n.Content, scalarNode(k, k), anyNode(t[k]))
		}
		return n
	case []any:
		seq := yamlSeq()
		for _, e := range t {
			seq.Content = append(seq.Content, anyNode(e))
		}
		return seq
	case string:
		return scalarNode(t, "")
	case bool:
		return boolNode(t)
	case int:
		return intNode(t)
	default:
		return scalarNode(fmt.Sprintf("%v", t), "")
	}
}

func sortedKeysStr(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func emptyDash(s string) string {
	if s == "" {
		return "(default)"
	}
	return s
}
