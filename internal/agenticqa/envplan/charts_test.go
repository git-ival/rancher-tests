package envplan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

func TestParseCPUMillis(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"4500m": {4500, true},
		"2":     {2000, true},
		"1.5":   {1500, true},
		"":      {0, false},
		"abc":   {0, false},
	}
	for in, exp := range cases {
		got, ok := parseCPUMillis(in)
		if ok != exp.ok || (ok && got != exp.want) {
			t.Errorf("parseCPUMillis(%q) = (%d,%v), want (%d,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestParseMemoryMiB(t *testing.T) {
	cases := map[string]struct {
		want int
		ok   bool
	}{
		"4000Mi": {4000, true},
		"2Gi":    {2048, true},
		"512Mi":  {512, true},
		"1G":     {953, true}, // 1e9 bytes ≈ 953 MiB
		"":       {0, false},
	}
	for in, exp := range cases {
		got, ok := parseMemoryMiB(in)
		if ok != exp.ok || (ok && got != exp.want) {
			t.Errorf("parseMemoryMiB(%q) = (%d,%v), want (%d,%v)", in, got, ok, exp.want, exp.ok)
		}
	}
}

func TestRancherVersionMatches(t *testing.T) {
	cases := []struct {
		constraint string
		minor      string
		want       bool
	}{
		{">= 2.14.0-0 < 2.15.0-0", "2.14", true},
		{">= 2.14.0-0 < 2.15.0-0", "2.15", false},
		{">= 2.14.0-0 < 2.15.0-0", "2.13", false},
		{">= 2.12.0-0 < 2.13.0-0", "2.12", true},
		{"", "2.14", false},
	}
	for _, c := range cases {
		if got := rancherVersionMatches(c.constraint, c.minor); got != c.want {
			t.Errorf("rancherVersionMatches(%q, %q) = %v, want %v", c.constraint, c.minor, got, c.want)
		}
	}
}

func TestParseRancherMinor(t *testing.T) {
	cases := map[string]string{
		"v2.14.3":            "2.14",
		"2.14":               "2.14",
		"${RANCHER_VERSION}": "",
		"":                   "",
	}
	for in, want := range cases {
		if got := parseRancherMinor(in); got != want {
			t.Errorf("parseRancherMinor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveFromCatalogFallback(t *testing.T) {
	cfg := envconfig.GenerateChartSizing()
	r := NewChartFootprintResolver(cfg, "v2.14.0")
	// longhorn lacks the annotation upstream; resolves from the curated catalog.
	fp, ok := r.Resolve("longhorn")
	if !ok {
		t.Fatal("expected longhorn footprint from catalog")
	}
	if fp.Source != types.ChartFootprintSourceCatalog {
		t.Errorf("want catalog source, got %s", fp.Source)
	}
	if fp.CPUMillis <= 0 || fp.MemoryMiB <= 0 {
		t.Errorf("catalog footprint should be non-zero, got %+v", fp)
	}
}

func TestResolveFromLocalChartAnnotation(t *testing.T) {
	// Build a fake local charts checkout with a Chart.yaml carrying the
	// requests annotations and a matching rancher-version constraint.
	dir := t.TempDir()
	chartsRoot := filepath.Join(dir, "charts")
	chartDir := filepath.Join(chartsRoot, "rancher-monitoring", "109.0.0+up80.9.1")
	if err := os.MkdirAll(chartDir, 0o755); err != nil {
		t.Fatal(err)
	}
	chartYAML := `annotations:
  catalog.cattle.io/requests-cpu: 4500m
  catalog.cattle.io/requests-memory: 4000Mi
  catalog.cattle.io/rancher-version: '>= 2.14.0-0 < 2.15.0-0'
apiVersion: v2
`
	if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte(chartYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := envconfig.GenerateChartSizing()
	cfg.Source.LocalPath = chartsRoot
	r := NewChartFootprintResolver(cfg, "v2.14.2")
	if r.LocalChartsRoot() == "" {
		t.Fatal("expected local charts root to be detected")
	}

	fp, ok := r.Resolve("rancher-monitoring")
	if !ok {
		t.Fatal("expected monitoring footprint from local annotation")
	}
	if fp.Source != types.ChartFootprintSourceAnnotation {
		t.Errorf("want annotation source, got %s", fp.Source)
	}
	if fp.CPUMillis != 4500 || fp.MemoryMiB != 4000 {
		t.Errorf("want 4500m/4000Mi, got %dm/%dMiB", fp.CPUMillis, fp.MemoryMiB)
	}
}

func TestReleaseBranch(t *testing.T) {
	cfg := envconfig.GenerateChartSizing()
	r := NewChartFootprintResolver(cfg, "v2.14.3")
	if got := r.ReleaseBranch(); got != "release-v2.14" {
		t.Errorf("want release-v2.14, got %s", got)
	}
}

func TestComputeSpecsWithCharts_BumpsWorker(t *testing.T) {
	pools := []types.NodeRequirement{
		{Etcd: true, ControlPlane: true, Worker: true, Quantity: 1},
	}
	charts := []types.ChartRequirement{
		{Name: "rancher-monitoring", Footprint: &types.ChartFootprint{CPUMillis: 4500, MemoryMiB: 4000, DiskGiB: 50}},
	}
	base := ComputeSpecs(types.ClusterRequirement{NodePools: clone(pools)}, nil)
	withCharts := ComputeSpecsWithCharts(types.ClusterRequirement{NodePools: clone(pools)}, nil, charts)

	bs := base.NodePools[0].Spec
	cs := withCharts.NodePools[0].Spec
	if cs.VCPUs <= bs.VCPUs {
		t.Errorf("chart should raise vCPUs: base=%d charts=%d", bs.VCPUs, cs.VCPUs)
	}
	if cs.MemoryGiB <= bs.MemoryGiB {
		t.Errorf("chart should raise memory: base=%d charts=%d", bs.MemoryGiB, cs.MemoryGiB)
	}
}

func TestComputeSpecsWithCharts_UnresolvedUsesDefault(t *testing.T) {
	pools := []types.NodeRequirement{{Worker: true, Quantity: 1}}
	charts := []types.ChartRequirement{{Name: "mystery-chart"}} // no footprint
	withCharts := ComputeSpecsWithCharts(types.ClusterRequirement{NodePools: clone(pools)}, nil, charts)
	base := ComputeSpecs(types.ClusterRequirement{NodePools: clone(pools)}, nil)
	if withCharts.NodePools[0].Spec.MemoryGiB <= base.NodePools[0].Spec.MemoryGiB {
		t.Error("unresolved chart should still raise sizing via conservative default")
	}
}

func TestChartFootprintLLMApply(t *testing.T) {
	charts := []types.ChartRequirement{{Name: "neuvector"}}
	res := ChartFootprintLLMResult{}
	res.Charts = append(res.Charts, struct {
		Name      string `json:"name"`
		CPUMillis int    `json:"cpu_millis"`
		MemoryMiB int    `json:"memory_mib"`
		DiskGiB   int    `json:"disk_gib"`
		Rationale string `json:"rationale"`
	}{Name: "neuvector", CPUMillis: 800, MemoryMiB: 1500})

	n := ApplyChartFootprintLLM(charts, res)
	if n != 1 {
		t.Fatalf("want 1 applied, got %d", n)
	}
	if charts[0].Footprint == nil || charts[0].Footprint.Source != types.ChartFootprintSourceLLM {
		t.Errorf("expected LLM footprint applied, got %+v", charts[0].Footprint)
	}
}
