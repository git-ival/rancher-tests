// Package envplan computes the minimum viable test environment required to run
// a set of identified tests. It statically analyses Go test sources from the
// rancher-tests repository to derive node-pool topology, Kubernetes distro,
// CNI, provider, networking and workload requirements, and provides an LLM
// fallback for tests whose requirements cannot be determined statically.
package envplan

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/types"
)

// Analysis is the result of statically analysing a single test file.
type Analysis struct {
	// Inconclusive is true when the analyzer found no environment signals at
	// all, meaning the caller should use the LLM fallback for this file.
	Inconclusive bool
	Cluster      types.ClusterRequirement
	Workloads    []types.WorkloadRequirement
	Charts       []types.ChartRequirement
	Notes        []string
}

// Named machine-pool constants exposed by actions/provisioninginput. Each maps
// to a role set with a default quantity of 1 (overridable via .Quantity = N).
var machinePoolRoles = map[string]types.NodeRequirement{
	"AllRolesMachinePool":         {Etcd: true, ControlPlane: true, Worker: true, Quantity: 1, Description: "all-roles"},
	"EtcdControlPlaneMachinePool": {Etcd: true, ControlPlane: true, Quantity: 1, Description: "etcd+controlplane"},
	"EtcdMachinePool":             {Etcd: true, Quantity: 1, Description: "etcd"},
	"ControlPlaneMachinePool":     {ControlPlane: true, Quantity: 1, Description: "controlplane"},
	"WorkerMachinePool":           {Worker: true, Quantity: 1, Description: "worker"},
	"WindowsMachinePool":          {Windows: true, Quantity: 1, Description: "windows"},
}

var (
	// provisioninginput.<Name>MachinePool
	reMachinePool = regexp.MustCompile(`provisioninginput\.([A-Za-z]+MachinePool)\b`)
	// nodeRoles[INDEX].MachinePoolConfig.Quantity = N  OR  pool[0].Quantity = N
	reQuantityIndexed = regexp.MustCompile(`\[(\d+)\][.\w]*?\.Quantity\s*=\s*(\d+)`)
	// Inline node struct literals. A declaration may contain several
	// brace-groups, e.g.
	//   []tfpConfig.Nodepool{{Quantity: 1, Etcd: true}, {Quantity: 1, Worker: true}}
	// reNodeLiteralDecl finds each Nodepool/NodeRoles declaration's full body,
	// and reInnerLiteral extracts every individual {...} element within it.
	reNodeLiteralDecl = regexp.MustCompile(`(?:Nodepool|NodeRoles)(\{[\s\S]*?\}\s*\})`)
	reInnerLiteral    = regexp.MustCompile(`\{([^{}]*(?:true|false|\d)[^{}]*)\}`)
	reLitQuantity     = regexp.MustCompile(`(?i)Quantity\s*:\s*(\d+)`)
	reLitEtcd         = regexp.MustCompile(`(?i)Etcd\s*:\s*true`)
	reLitCP           = regexp.MustCompile(`(?i)Control[Pp]lane\s*:\s*true`)
	reLitWorker       = regexp.MustCompile(`(?i)Worker\s*:\s*true`)
	reLitWindows      = regexp.MustCompile(`(?i)Windows\s*:\s*true`)
	// defaults.RKE2 / defaults.K3S / defaults.RKE1
	reDistro = regexp.MustCompile(`defaults\.(RKE2|K3S|RKE1)\b`)
	// CNI hints in source/strings
	reCNI = regexp.MustCompile(`(?i)\b(calico|cilium|canal|flannel|multus|weave)\b`)
	// provider name constants
	reProvider = regexp.MustCompile(`provisioninginput\.(AWS|Azure|DO|Harvester|Linode|Google|Vsphere|VsphereCloud|External)ProviderName\b`)
	// LocalClusterAuthEndpoint (ACE)
	reACE = regexp.MustCompile(`LocalClusterAuthEndpoint`)
	// hardened cluster
	reHardened = regexp.MustCompile(`(?i)\bhardened\b`)
	// PSACT
	rePSACT = regexp.MustCompile(`provisioninginput\.(RancherPrivileged|RancherRestricted|RancherBaseline)\b`)
	// dual-stack / ipv6
	reIPv6 = regexp.MustCompile(`(?i)\b(ipv6|dualstack|dual-stack|StackPreference)\b`)
	// windows workloads
	reWindows = regexp.MustCompile(`(?i)\bwindows\b`)
)

// workloadImports maps an imported rancher-tests workload package suffix to a
// workload kind. Presence of the import is a strong signal the test deploys
// that workload.
var workloadImports = map[string]string{
	"workloads/deployment":  types.WorkloadDeployment,
	"workloads/pods":        types.WorkloadPod,
	"workloads/daemonset":   types.WorkloadDaemonSet,
	"workloads/statefulset": types.WorkloadStatefulSet,
	"workloads/cronjob":     types.WorkloadCronJob,
	"workloads/job":         types.WorkloadJob,
	"ingress":               types.WorkloadIngress,
}

// chartConstants maps an actions/charts Go name-constant identifier to the
// canonical Rancher chart name (matching the rancher/charts directory name).
// Presence of the constant in a test source is a strong signal the test
// installs that chart, which dominates cluster resource sizing.
var chartConstants = map[string]string{
	"RancherMonitoringName": "rancher-monitoring",
	"RancherLoggingName":    "rancher-logging",
	"RancherIstioName":      "rancher-istio",
	"LonghornChartName":     "longhorn",
	"CISBenchmarkName":      "rancher-cis-benchmark",
	"NeuVectorChartName":    "neuvector",
	"RancherGatekeeperName": "rancher-gatekeeper",
	"RancherAlertingName":   "rancher-alerting-drivers",
	"ComplianceName":        "rancher-compliance",
	"RancherBackupName":     "rancher-backup",
}

// reChartConst matches charts.<ConstName> references in the source.
var reChartConst = regexp.MustCompile(`charts\.([A-Za-z][A-Za-z0-9]*)\b`)

// providerConstToName maps the Go const identifier to the cattle-config value.
var providerConstToName = map[string]string{
	"AWS":          types.ProviderAWS,
	"Azure":        types.ProviderAzure,
	"DO":           types.ProviderDO,
	"Harvester":    types.ProviderHarvester,
	"Linode":       types.ProviderLinode,
	"Google":       types.ProviderGoogle,
	"Vsphere":      types.ProviderVsphere,
	"VsphereCloud": types.ProviderVsphereCloud,
	"External":     types.ProviderExternal,
}

var distroConstToName = map[string]string{
	"RKE2": types.DistroRKE2,
	"K3S":  types.DistroK3S,
	"RKE1": types.DistroRKE1,
}

var psactConstToName = map[string]string{
	"RancherPrivileged": types.PSACTPrivileged,
	"RancherRestricted": types.PSACTRestricted,
	"RancherBaseline":   types.PSACTBaseline,
}

// AnalyzeFile statically analyses a single test file at testRepoRoot/relPath.
// It returns an Analysis; if the file cannot be read or contains no recognised
// environment signals, Inconclusive is set so the caller can fall back to LLM.
func AnalyzeFile(testRepoRoot, relPath string) Analysis {
	full := filepath.Join(testRepoRoot, relPath)
	data, err := os.ReadFile(full)
	if err != nil {
		return Analysis{Inconclusive: true, Notes: []string{"file not readable: " + err.Error()}}
	}
	return AnalyzeSource(string(data))
}

// AnalyzeSource performs the static analysis on raw Go source text. It is
// separated from AnalyzeFile to make unit testing straightforward.
func AnalyzeSource(src string) Analysis {
	a := Analysis{}
	cluster := &a.Cluster

	// --- Node pools from named machine-pool constants. ---
	// Find the order of pool constants as they appear, then apply any indexed
	// quantity overrides ([0].Quantity = 3, etc.).
	var pools []types.NodeRequirement
	for _, m := range reMachinePool.FindAllStringSubmatch(src, -1) {
		role, ok := machinePoolRoles[m[1]]
		if !ok {
			continue
		}
		pools = append(pools, role)
	}

	// Apply indexed quantity overrides where present.
	for _, m := range reQuantityIndexed.FindAllStringSubmatch(src, -1) {
		idx, err1 := strconv.Atoi(m[1])
		qty, err2 := strconv.Atoi(m[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if idx >= 0 && idx < len(pools) && qty > 0 {
			pools[idx].Quantity = qty
		}
	}

	// If no named machine-pool constants were found, fall back to parsing inline
	// Nodepool{...}/NodeRoles{...} struct literals. These appear in test tables
	// of mutually-exclusive topologies; we take the superset (max quantity per
	// role signature) so the resulting environment can run every case.
	if len(pools) == 0 {
		pools = parseNodeLiterals(src)
	}

	if len(pools) > 0 {
		cluster.NodePools = pools
		cluster.Downstream = true
	}

	// --- Kubernetes distro. ---
	if m := reDistro.FindStringSubmatch(src); m != nil {
		cluster.KubernetesDistro = distroConstToName[m[1]]
		cluster.Downstream = true
	}

	// --- Provider. ---
	if m := reProvider.FindStringSubmatch(src); m != nil {
		cluster.Provider = providerConstToName[m[1]]
	}

	// --- CNI. ---
	if m := reCNI.FindStringSubmatch(src); m != nil {
		cluster.CNI = strings.ToLower(m[1])
	}

	// --- PSACT. ---
	if m := rePSACT.FindStringSubmatch(src); m != nil {
		cluster.PSACT = psactConstToName[m[1]]
	}

	// --- Hardened. ---
	if reHardened.MatchString(src) {
		cluster.Hardened = true
	}

	// --- Networking: ACE and dual-stack. ---
	var net *types.NetworkingRequirement
	if reACE.MatchString(src) {
		if net == nil {
			net = &types.NetworkingRequirement{}
		}
		net.LocalClusterAuthEndpoint = true
		a.Notes = append(a.Notes, "requires LocalClusterAuthEndpoint (ACE)")
	}
	if reIPv6.MatchString(src) {
		if net == nil {
			net = &types.NetworkingRequirement{}
		}
		net.StackPreference = types.StackPreferenceDual
		a.Notes = append(a.Notes, "requires dual-stack / ipv6 networking")
	}
	cluster.Networking = net

	// --- Workloads from imports / references. ---
	seenWorkload := map[string]struct{}{}
	for needle, kind := range workloadImports {
		if strings.Contains(src, needle) {
			if _, dup := seenWorkload[kind]; dup {
				continue
			}
			seenWorkload[kind] = struct{}{}
			a.Workloads = append(a.Workloads, types.WorkloadRequirement{
				Kind:        kind,
				Description: "test imports " + needle,
			})
		}
	}

	// --- Charts installed by the test (dominant sizing signal). ---
	seenChart := map[string]struct{}{}
	for _, m := range reChartConst.FindAllStringSubmatch(src, -1) {
		name, ok := chartConstants[m[1]]
		if !ok {
			continue
		}
		if _, dup := seenChart[name]; dup {
			continue
		}
		seenChart[name] = struct{}{}
		a.Charts = append(a.Charts, types.ChartRequirement{
			Name:     name,
			Detected: "charts." + m[1],
		})
	}

	// --- Windows note (affects node pools / images). ---
	if reWindows.MatchString(src) {
		a.Notes = append(a.Notes, "references Windows; may require Windows worker nodes")
	}

	cluster.TotalNodes = totalNodes(cluster.NodePools)

	// Inconclusive only when we found NOTHING actionable.
	if len(cluster.NodePools) == 0 &&
		cluster.KubernetesDistro == "" &&
		cluster.Provider == "" &&
		cluster.CNI == "" &&
		cluster.PSACT == "" &&
		!cluster.Hardened &&
		net == nil &&
		len(a.Workloads) == 0 &&
		len(a.Charts) == 0 {
		a.Inconclusive = true
	}

	return a
}

// parseNodeLiterals extracts node pools from inline Nodepool{...}/NodeRoles{...}
// struct literals. Multiple literals across a file (e.g. test-table
// alternatives) are merged by role signature, keeping the maximum quantity, so
// the derived environment is a superset that can run every alternative.
func parseNodeLiterals(src string) []types.NodeRequirement {
	byRole := map[string]types.NodeRequirement{}
	var order []string

	for _, decl := range reNodeLiteralDecl.FindAllStringSubmatch(src, -1) {
		declBody := decl[1]
		for _, inner := range reInnerLiteral.FindAllStringSubmatch(declBody, -1) {
			body := inner[1]
			var n types.NodeRequirement
			n.Etcd = reLitEtcd.MatchString(body)
			n.ControlPlane = reLitCP.MatchString(body)
			n.Worker = reLitWorker.MatchString(body)
			n.Windows = reLitWindows.MatchString(body)
			if !n.Etcd && !n.ControlPlane && !n.Worker && !n.Windows {
				continue // not a node-role literal
			}
			n.Quantity = 1
			if q := reLitQuantity.FindStringSubmatch(body); q != nil {
				if v, err := strconv.Atoi(q[1]); err == nil && v > 0 {
					n.Quantity = v
				}
			}
			n.Description = inlineRoleDescription(n)

			key := inlineRoleSignature(n)
			if existing, ok := byRole[key]; ok {
				if n.Quantity > existing.Quantity {
					existing.Quantity = n.Quantity
					byRole[key] = existing
				}
			} else {
				byRole[key] = n
				order = append(order, key)
			}
		}
	}

	sort.Strings(order)
	out := make([]types.NodeRequirement, 0, len(order))
	for _, k := range order {
		out = append(out, byRole[k])
	}
	return out
}

func inlineRoleSignature(n types.NodeRequirement) string {
	var roles []string
	if n.Etcd {
		roles = append(roles, types.RoleEtcd)
	}
	if n.ControlPlane {
		roles = append(roles, types.RoleControlPlane)
	}
	if n.Worker {
		roles = append(roles, types.RoleWorker)
	}
	if n.Windows {
		roles = append(roles, types.RoleWindows)
	}
	return strings.Join(roles, "+")
}

func inlineRoleDescription(n types.NodeRequirement) string {
	return inlineRoleSignature(n)
}

func totalNodes(pools []types.NodeRequirement) int {
	total := 0
	for _, p := range pools {
		total += p.Quantity
	}
	return total
}
