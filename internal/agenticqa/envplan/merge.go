package envplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/types"
)

const (
	// roleSignatureNone is returned by roleSignature when a pool has no roles.
	roleSignatureNone = "(none)"
	// consistencyKeyDefault is the discriminator key for requirements that pin
	// no cluster dimensions, making them compatible with any environment.
	consistencyKeyDefault = "default"
	// groupKeyUngrouped is the fallback group key for requirements that have
	// no associated Jenkins jobs or build tags.
	groupKeyUngrouped = "ungrouped"
)

// Requirement bundles a test file's path with its derived cluster/workload
// requirements and how they were derived ("static" or "llm").
type Requirement struct {
	File      string
	Cluster   types.ClusterRequirement
	Workloads []types.WorkloadRequirement
	Charts    []types.ChartRequirement
	DerivedBy string
	Jobs      []string
	BuildTags []string
}

// mergeConflict reports an incompatibility that prevents two requirements from
// sharing a single environment.
type mergeConflict struct {
	field string
	a, b  string
}

func (c mergeConflict) String() string {
	return fmt.Sprintf("%s mismatch: %q vs %q", c.field, c.a, c.b)
}

// MergeClusters merges a set of cluster requirements into one minimum viable
// cluster (taking the superset / maximum of every dimension). It returns the
// merged cluster, any warnings (resolved soft conflicts), and any hard
// conflicts that make a single environment unsafe.
//
// Hard conflicts are: differing non-empty Provider, KubernetesDistro, CNI,
// PSACT, or mixed Hardened settings. Node pools are merged by role signature,
// taking the maximum quantity per role set. Networking flags are OR-ed.
func MergeClusters(reqs []types.ClusterRequirement) (merged types.ClusterRequirement, warnings []string, conflicts []mergeConflict) {
	poolByRole := map[string]types.NodeRequirement{}
	var roleOrder []string

	for _, r := range reqs {
		// Scalar string dimensions: detect hard conflicts, otherwise adopt.
		merged.KubernetesDistro, conflicts = mergeScalar("kubernetes_distro", merged.KubernetesDistro, r.KubernetesDistro, conflicts)
		merged.Provider, conflicts = mergeScalar("provider", merged.Provider, r.Provider, conflicts)
		merged.NodeProvider, conflicts = mergeScalar("node_provider", merged.NodeProvider, r.NodeProvider, conflicts)
		merged.CNI, conflicts = mergeScalar("cni", merged.CNI, r.CNI, conflicts)
		merged.PSACT, conflicts = mergeScalar("psact", merged.PSACT, r.PSACT, conflicts)

		// Kubernetes version: take the highest non-empty (lexical max is a
		// reasonable proxy; soft conflict only).
		if r.KubernetesVersion != "" {
			if merged.KubernetesVersion == "" {
				merged.KubernetesVersion = r.KubernetesVersion
			} else if r.KubernetesVersion != merged.KubernetesVersion {
				warnings = append(warnings, fmt.Sprintf(
					"kubernetes_version differs (%q vs %q); using %q",
					merged.KubernetesVersion, r.KubernetesVersion, maxStr(merged.KubernetesVersion, r.KubernetesVersion)))
				merged.KubernetesVersion = maxStr(merged.KubernetesVersion, r.KubernetesVersion)
			}
		}

		// Hardened: any hardened requirement makes the merged cluster hardened,
		// but mixing hardened and non-hardened is a hard conflict because a
		// hardened cluster can break non-hardened tests.
		if r.Hardened {
			merged.Hardened = true
		}

		// Downstream: any downstream requirement implies downstream.
		if r.Downstream {
			merged.Downstream = true
		}

		// Networking: OR the boolean flags, adopt first non-empty strings.
		merged.Networking = mergeNetworking(merged.Networking, r.Networking)

		// Node pools: key by role signature, keep max quantity AND the
		// element-wise max recommended spec so the merged pool can satisfy the
		// most demanding test sharing that role signature.
		for _, p := range r.NodePools {
			key := roleSignature(p)
			if existing, ok := poolByRole[key]; ok {
				if p.Quantity > existing.Quantity {
					existing.Quantity = p.Quantity
				}
				existing.Spec = MaxSpec(existing.Spec, p.Spec)
				poolByRole[key] = existing
			} else {
				poolByRole[key] = p
				roleOrder = append(roleOrder, key)
			}
		}
	}

	// Hardened-vs-not is a hard conflict only when at least one req is hardened
	// and at least one is explicitly not hardened with downstream nodes.
	if merged.Hardened {
		for _, r := range reqs {
			if !r.Hardened && r.Downstream {
				conflicts = append(conflicts, mergeConflict{field: "hardened", a: "true", b: "false"})
				break
			}
		}
	}

	// Emit merged node pools in a stable order (role signature sort).
	sort.Strings(roleOrder)
	for _, key := range roleOrder {
		merged.NodePools = append(merged.NodePools, poolByRole[key])
	}
	merged.TotalNodes = totalNodes(merged.NodePools)

	return merged, warnings, conflicts
}

// mergeScalar adopts b into a when a is empty; records a hard conflict when
// both are non-empty and differ.
func mergeScalar(field, a, b string, conflicts []mergeConflict) (string, []mergeConflict) {
	if b == "" {
		return a, conflicts
	}
	if a == "" {
		return b, conflicts
	}
	if a != b {
		conflicts = append(conflicts, mergeConflict{field: field, a: a, b: b})
	}
	return a, conflicts
}

func mergeNetworking(a, b *types.NetworkingRequirement) *types.NetworkingRequirement {
	if a == nil && b == nil {
		return nil
	}
	out := &types.NetworkingRequirement{}
	if a != nil {
		*out = *a
	}
	if b != nil {
		if b.LocalClusterAuthEndpoint {
			out.LocalClusterAuthEndpoint = true
		}
		if out.StackPreference == "" {
			out.StackPreference = b.StackPreference
		}
		if out.ClusterCIDR == "" {
			out.ClusterCIDR = b.ClusterCIDR
		}
		if out.ServiceCIDR == "" {
			out.ServiceCIDR = b.ServiceCIDR
		}
	}
	return out
}

// roleSignature returns a stable key describing a node pool's role set (not its
// quantity), so pools with the same roles merge into one with the max quantity.
func roleSignature(n types.NodeRequirement) string {
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
	if len(roles) == 0 {
		return roleSignatureNone
	}
	return strings.Join(roles, "+")
}

func maxStr(a, b string) string {
	if a >= b {
		return a
	}
	return b
}

// mergeWorkloads de-duplicates workloads by kind+name.
func mergeWorkloads(sets ...[]types.WorkloadRequirement) []types.WorkloadRequirement {
	seen := map[string]struct{}{}
	var out []types.WorkloadRequirement
	for _, set := range sets {
		for _, w := range set {
			key := w.Kind + "/" + w.Name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, w)
		}
	}
	return out
}

// mergeCharts de-duplicates charts by name.
func mergeCharts(sets ...[]types.ChartRequirement) []types.ChartRequirement {
	seen := map[string]struct{}{}
	var out []types.ChartRequirement
	for _, set := range sets {
		for _, c := range set {
			if _, ok := seen[c.Name]; ok {
				continue
			}
			seen[c.Name] = struct{}{}
			out = append(out, c)
		}
	}
	return out
}

// BuildPlan turns per-file requirements into an EnvironmentPlan. It first
// attempts the single-environment strategy (merging everything). If merging
// produces hard conflicts, it falls back to per-group environments keyed by
// Jenkins job (or build tag when no job is known).
//
// groupKeyFor returns the grouping key for a requirement under the per-group
// strategy; pass nil to use the default (first Jenkins job, else first build
// tag, else "ungrouped").
func BuildPlan(reqs []Requirement, prNumber int) types.EnvironmentPlan {
	plan := types.EnvironmentPlan{PRNumber: prNumber}

	// Attempt single environment.
	clusters := make([]types.ClusterRequirement, 0, len(reqs))
	for _, r := range reqs {
		clusters = append(clusters, r.Cluster)
	}
	merged, warnings, conflicts := MergeClusters(clusters)

	if len(conflicts) == 0 {
		plan.Strategy = types.PlanStrategySingle
		g := types.EnvironmentGroup{
			Name:      types.PlanGroupNameAll,
			Cluster:   merged,
			Warnings:  warnings,
			DerivedBy: map[string]string{},
		}
		jobSet := map[string]struct{}{}
		tagSet := map[string]struct{}{}
		var workloadSets [][]types.WorkloadRequirement
		var chartSets [][]types.ChartRequirement
		for _, r := range reqs {
			g.TestFiles = append(g.TestFiles, r.File)
			g.DerivedBy[r.File] = r.DerivedBy
			workloadSets = append(workloadSets, r.Workloads)
			chartSets = append(chartSets, r.Charts)
			for _, j := range r.Jobs {
				jobSet[j] = struct{}{}
			}
			for _, t := range r.BuildTags {
				tagSet[t] = struct{}{}
			}
		}
		g.Workloads = mergeWorkloads(workloadSets...)
		g.Charts = mergeCharts(chartSets...)
		g.JenkinsJobs = sortedKeys(jobSet)
		g.BuildTags = sortedKeys(tagSet)
		sort.Strings(g.TestFiles)
		plan.Groups = []types.EnvironmentGroup{g}
		return plan
	}

	// Fall back to per-group.
	plan.Strategy = types.PlanStrategyPerGroup
	var conflictMsgs []string
	for _, c := range conflicts {
		conflictMsgs = append(conflictMsgs, c.String())
	}

	grouped := map[string][]Requirement{}
	var order []string
	for _, r := range reqs {
		key := defaultGroupKey(r)
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], r)
	}
	sort.Strings(order)

	// Within each job-group, residual conflicts (e.g. RKE2 vs K3S tests that
	// happen to share the same Jenkins job) are resolved by sub-splitting on a
	// conflict-aware key so that every emitted environment is internally
	// consistent. The emitted group name is suffixed with the discriminator.
	for _, key := range order {
		members := grouped[key]

		subGroups, subOrder := subSplitConsistent(members)
		multiSub := len(subOrder) > 1

		for _, subKey := range subOrder {
			subMembers := subGroups[subKey]
			var cs []types.ClusterRequirement
			for _, m := range subMembers {
				cs = append(cs, m.Cluster)
			}
			gMerged, gWarn, gConflicts := MergeClusters(cs)

			name := key
			if multiSub {
				name = key + "/" + subKey
			}
			g := types.EnvironmentGroup{
				Name:      name,
				Cluster:   gMerged,
				Warnings:  gWarn,
				DerivedBy: map[string]string{},
			}
			members := subMembers
			// Annotate why we split.
			g.Warnings = append(g.Warnings, "split into per-group strategy due to: "+strings.Join(conflictMsgs, "; "))
			if multiSub {
				g.Warnings = append(g.Warnings, fmt.Sprintf("sub-split job group %q on discriminator %q for internal consistency", key, subKey))
			}
			for _, c := range gConflicts {
				g.Warnings = append(g.Warnings, "residual intra-group conflict: "+c.String())
			}

			jobSet := map[string]struct{}{}
			tagSet := map[string]struct{}{}
			var workloadSets [][]types.WorkloadRequirement
			var chartSets [][]types.ChartRequirement
			for _, m := range members {
				g.TestFiles = append(g.TestFiles, m.File)
				g.DerivedBy[m.File] = m.DerivedBy
				workloadSets = append(workloadSets, m.Workloads)
				chartSets = append(chartSets, m.Charts)
				for _, j := range m.Jobs {
					jobSet[j] = struct{}{}
				}
				for _, t := range m.BuildTags {
					tagSet[t] = struct{}{}
				}
			}
			g.Workloads = mergeWorkloads(workloadSets...)
			g.Charts = mergeCharts(chartSets...)
			g.JenkinsJobs = sortedKeys(jobSet)
			g.BuildTags = sortedKeys(tagSet)
			sort.Strings(g.TestFiles)
			plan.Groups = append(plan.Groups, g)
		}
	}
	return plan
}

// subSplitConsistent partitions a job-group's members into internally
// consistent sub-groups keyed by the dimensions that cause hard merge
// conflicts (distro, provider, CNI, PSACT, hardened). Members that share the
// same discriminator can safely merge into one environment. When the whole
// group is already consistent, it returns a single sub-group keyed "".
func subSplitConsistent(members []Requirement) (map[string][]Requirement, []string) {
	groups := map[string][]Requirement{}
	var order []string
	for _, m := range members {
		key := consistencyKey(m.Cluster)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], m)
	}
	if len(order) <= 1 {
		// Single consistent sub-group: collapse to "" so the name isn't suffixed.
		return map[string][]Requirement{"": members}, []string{""}
	}
	sort.Strings(order)
	return groups, order
}

// consistencyKey returns a discriminator describing the cluster dimensions
// that, if they differ, prevent two requirements from sharing one environment.
// Only non-empty dimensions contribute, so a test that pins nothing is
// compatible with anything and lands in a shared default bucket.
func consistencyKey(c types.ClusterRequirement) string {
	var parts []string
	if c.KubernetesDistro != "" {
		parts = append(parts, "distro="+c.KubernetesDistro)
	}
	if c.Provider != "" {
		parts = append(parts, "provider="+c.Provider)
	}
	if c.CNI != "" {
		parts = append(parts, "cni="+c.CNI)
	}
	if c.PSACT != "" {
		parts = append(parts, "psact="+c.PSACT)
	}
	if c.Hardened {
		parts = append(parts, "hardened")
	}
	if len(parts) == 0 {
		return consistencyKeyDefault
	}
	return strings.Join(parts, ",")
}

// defaultGroupKey keys a requirement by its first Jenkins job, else first build
// tag, else "ungrouped".
func defaultGroupKey(r Requirement) string {
	if len(r.Jobs) > 0 {
		sorted := append([]string(nil), r.Jobs...)
		sort.Strings(sorted)
		return sorted[0]
	}
	if len(r.BuildTags) > 0 {
		sorted := append([]string(nil), r.BuildTags...)
		sort.Strings(sorted)
		return sorted[0]
	}
	return groupKeyUngrouped
}

func sortedKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
