package envplan

import (
	"fmt"
	"strings"

	"github.com/rancher/tests/internal/agenticqa/types"
)

// ChartFootprintLLMResult is the LLM's estimated footprints for charts that
// could not be resolved from Chart.yaml annotations or the curated catalog.
type ChartFootprintLLMResult struct {
	Charts []struct {
		Name      string `json:"name"`
		CPUMillis int    `json:"cpu_millis"`
		MemoryMiB int    `json:"memory_mib"`
		DiskGiB   int    `json:"disk_gib"`
		Rationale string `json:"rationale"`
	} `json:"charts"`
}

// BuildChartFootprintSystemPrompt returns the system prompt for estimating the
// total resource requests of named Rancher Helm charts.
func BuildChartFootprintSystemPrompt(projectDisplayName string) string {
	return fmt.Sprintf(`You are a Kubernetes capacity expert for the %s project.
You are given a list of Rancher Helm chart names. For each chart, estimate the
TOTAL resource requests (summed across all of the chart's pods/containers) that
a single default installation needs in order to run reliably.

Base your estimates on the well-known resource profiles of these charts (e.g.
rancher-monitoring runs Prometheus/Grafana/Alertmanager and is heavy; longhorn
runs per-node managers; smaller operators are light). Report CPU in millicores
and memory in MiB. Include a modest disk estimate (GiB) when the chart uses
persistent storage; otherwise 0.

Respond ONLY with JSON matching this schema. Echo each chart back by its exact
name:
{
  "charts": [
    {"name": "string", "cpu_millis": int, "memory_mib": int, "disk_gib": int, "rationale": "string"}
  ]
}`, projectDisplayName)
}

// BuildChartFootprintUserMessage lists the charts to estimate.
func BuildChartFootprintUserMessage(chartNames []string, releaseBranch string) string {
	var b strings.Builder
	if releaseBranch != "" {
		fmt.Fprintf(&b, "Target rancher/charts release branch: %s\n\n", releaseBranch)
	}
	b.WriteString("Estimate total resource requests for these charts:\n")
	for _, n := range chartNames {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	b.WriteString("\nReturn one entry per chart, keyed by the exact name shown above.")
	return b.String()
}

// ApplyChartFootprintLLM overlays LLM-estimated footprints onto the matching
// charts (by name) that do not yet have a resolved footprint. Returns the
// number of charts updated.
func ApplyChartFootprintLLM(charts []types.ChartRequirement, res ChartFootprintLLMResult) int {
	byName := map[string]int{}
	for i := range charts {
		byName[charts[i].Name] = i
	}
	updated := 0
	for _, c := range res.Charts {
		idx, ok := byName[c.Name]
		if !ok || charts[idx].Footprint != nil {
			continue
		}
		if c.CPUMillis <= 0 && c.MemoryMiB <= 0 {
			continue
		}
		charts[idx].Footprint = &types.ChartFootprint{
			CPUMillis: c.CPUMillis,
			MemoryMiB: c.MemoryMiB,
			DiskGiB:   c.DiskGiB,
			Source:    types.ChartFootprintSourceLLM,
		}
		updated++
	}
	return updated
}
