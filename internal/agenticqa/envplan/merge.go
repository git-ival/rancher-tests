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

// MergeClusters combines compatible requirements and reports conflicts.
func MergeClusters(reqs []types.ClusterRequirement) (merged types.ClusterRequirement, warnings []string, conflicts []mergeConflict) {
	poolByRole := map[string]types.NodeRequirement{}
	var roleOrder []string

	for _, r := range reqs {
		merged.KubernetesDistro, conflicts = mergeScalar("kubernetes_distro", merged.KubernetesDistro, r.KubernetesDistro, conflicts)
		merged.Provider, conflicts = mergeScalar("provider", merged.Provider, r.Provider, conflicts)
		merged.NodeProvider, conflicts = mergeScalar("node_provider", merged.NodeProvider, r.NodeProvider, conflicts)
		merged.CNI, conflicts = mergeScalar("cni", merged.CNI, r.CNI, conflicts)
		merged.PSACT, conflicts = mergeScalar("psact", merged.PSACT, r.PSACT, conflicts)

		// Kubernetes version mismatch is soft; use the lexical maximum.
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

		if r.Hardened {
			merged.Hardened = true
		}

		if r.Downstream {
			merged.Downstream = true
		}

		merged.Networking = mergeNetworking(merged.Networking, r.Networking)

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

	// Mixed hardened and explicit non-hardened downstream requirements conflict.
	if merged.Hardened {
		for _, r := range reqs {
			if !r.Hardened && r.Downstream {
				conflicts = append(conflicts, mergeConflict{field: "hardened", a: "true", b: "false"})
				break
			}
		}
	}

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

// BuildPlan uses one environment when safe, otherwise groups by job or tag.
func BuildPlan(reqs []Requirement, prNumber int) types.EnvironmentPlan {
	plan := types.EnvironmentPlan{PRNumber: prNumber}

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

	// Split inconsistent job groups by conflicting dimensions.
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

// subSplitConsistent partitions a group by hard-conflict dimensions.
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
		return map[string][]Requirement{"": members}, []string{""}
	}
	sort.Strings(order)
	return groups, order
}

// consistencyKey identifies pinned dimensions that prevent sharing.
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
