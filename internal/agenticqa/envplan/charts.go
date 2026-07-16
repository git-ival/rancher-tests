package envplan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/rancher/tests/internal/agenticqa/envconfig"
	"github.com/rancher/tests/internal/agenticqa/types"
)

// ChartFootprintResolver resolves a Helm chart's resource footprint using a
// three-tier cascade:
//
//  1. Chart.yaml annotations (catalog.cattle.io/requests-cpu and
//     requests-memory) read from a local rancher/charts checkout when
//     available. This is authoritative but only present on some charts.
//  2. The curated fallback catalog in pipeline_env.json, covering the heavy
//     charts whose Chart.yaml lacks the annotation and providing an offline
//     backstop.
//  3. The LLM (handled by the caller via ResolveWithLLMFallback), only for
//     charts unknown to both other tiers.
//
// The resolver derives the relevant rancher/charts release branch and chart
// version from the target Rancher version using the catalog.cattle.io/
// rancher-version constraint published in each Chart.yaml.
type ChartFootprintResolver struct {
	cfg envconfig.ChartSizingConfig
	// rancherMinor is the target Rancher "major.minor" (e.g. "2.14").
	rancherMinor string
	// chartsRoot is the resolved local charts directory (…/charts), or "" if
	// no local checkout is available.
	chartsRoot string
}

// chart-version annotation keys.
const (
	annRequestsCPU    = "catalog.cattle.io/requests-cpu"
	annRequestsMemory = "catalog.cattle.io/requests-memory"
	annRancherVersion = "catalog.cattle.io/rancher-version"
)

// reRancherMinor extracts "major.minor" from a Rancher version like
// "v2.14.3", "2.14", "${RANCHER_VERSION}" (no match → "").
var reRancherMinor = regexp.MustCompile(`(\d+)\.(\d+)`)

// NewChartFootprintResolver builds a resolver. rancherVersion may be a concrete
// version ("v2.14.3"), a bare "major.minor", or an unresolved ${VAR}; when it
// does not contain a parseable major.minor the config's DefaultRancherMinor is
// used. It auto-detects a local rancher/charts checkout from the configured
// LocalPath or common sibling locations of the workspace.
func NewChartFootprintResolver(cfg envconfig.ChartSizingConfig, rancherVersion string) *ChartFootprintResolver {
	return NewChartFootprintResolverForMinor(cfg, parseRancherMinor(rancherVersion))
}

// NewChartFootprintResolverForMinor builds a resolver from an already-resolved
// Rancher "major.minor" (e.g. "2.15"). An empty minor falls back to the
// config's DefaultRancherMinor. Prefer this over NewChartFootprintResolver
// when the minor has been authoritatively resolved (e.g. by envversions), so
// the chart branch tracks the true target Rancher version rather than a
// version placeholder.
func NewChartFootprintResolverForMinor(cfg envconfig.ChartSizingConfig, minor string) *ChartFootprintResolver {
	if minor == "" {
		minor = cfg.Source.DefaultRancherMinor
	}
	r := &ChartFootprintResolver{cfg: cfg, rancherMinor: minor}
	r.chartsRoot = detectChartsRoot(cfg.Source.LocalPath)
	return r
}

// LocalChartsRoot returns the resolved local charts directory, or "" if none.
func (r *ChartFootprintResolver) LocalChartsRoot() string { return r.chartsRoot }

// ReleaseBranch returns the rancher/charts release branch for the target
// Rancher minor (e.g. "release-v2.14"), or "" if the minor is unknown.
func (r *ChartFootprintResolver) ReleaseBranch() string {
	if r.rancherMinor == "" {
		return ""
	}
	return "release-v" + r.rancherMinor
}

// Resolve returns the footprint for a chart name and whether it was found via
// the annotation or curated catalog (the non-LLM tiers). LLM fallback is the
// caller's responsibility (it needs an llm client and context).
func (r *ChartFootprintResolver) Resolve(name string) (*types.ChartFootprint, bool) {
	// Tier 1: local Chart.yaml annotation.
	if r.chartsRoot != "" {
		if fp, ok := r.resolveFromLocalChart(name); ok {
			return fp, true
		}
	}
	// Tier 2: curated catalog.
	if spec, ok := r.cfg.Catalog[name]; ok {
		return &types.ChartFootprint{
			CPUMillis: spec.CPUMillis,
			MemoryMiB: spec.MemoryMiB,
			DiskGiB:   spec.DiskGiB,
			Source:    types.ChartFootprintSourceCatalog,
		}, true
	}
	return nil, false
}

// resolveFromLocalChart reads the chart's Chart.yaml (best matching version for
// the target Rancher minor) and extracts the requests-cpu/requests-memory
// annotations. Returns false when the chart dir, a matching version, or the
// annotations are absent.
func (r *ChartFootprintResolver) resolveFromLocalChart(name string) (*types.ChartFootprint, bool) {
	chartDir := filepath.Join(r.chartsRoot, name)
	versions, err := os.ReadDir(chartDir)
	if err != nil {
		return nil, false
	}
	// Collect version subdirectories, preferring those whose leading number
	// tracks the target Rancher minor; sort descending so the newest wins.
	var versionDirs []string
	for _, e := range versions {
		if e.IsDir() {
			versionDirs = append(versionDirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(versionDirs)))

	// First pass: a version whose Chart.yaml rancher-version constraint matches
	// the target minor AND that carries the requests annotations.
	var fallback *types.ChartFootprint
	for _, v := range versionDirs {
		ann := readChartAnnotations(filepath.Join(chartDir, v, "Chart.yaml"))
		if ann == nil {
			continue
		}
		fp := footprintFromAnnotations(ann)
		if fp == nil {
			continue
		}
		if rancherVersionMatches(ann[annRancherVersion], r.rancherMinor) {
			return fp, true
		}
		if fallback == nil {
			fallback = fp // newest annotated version, regardless of constraint
		}
	}
	if fallback != nil {
		return fallback, true
	}
	return nil, false
}

// readChartAnnotations parses a Chart.yaml and returns its annotations map.
func readChartAnnotations(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Annotations map[string]string `yaml:"annotations"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	return doc.Annotations
}

// footprintFromAnnotations builds a footprint from the requests-cpu/-memory
// annotations, or nil when either is missing/unparseable.
func footprintFromAnnotations(ann map[string]string) *types.ChartFootprint {
	cpu, okC := parseCPUMillis(ann[annRequestsCPU])
	mem, okM := parseMemoryMiB(ann[annRequestsMemory])
	if !okC || !okM {
		return nil
	}
	return &types.ChartFootprint{
		CPUMillis: cpu,
		MemoryMiB: mem,
		Source:    types.ChartFootprintSourceAnnotation,
	}
}

// rancherVersionMatches reports whether a constraint like
// ">= 2.14.0-0 < 2.15.0-0" includes the given "major.minor" (e.g. "2.14").
// It is intentionally lenient: it checks that the target minor's ".0" version
// falls within the lower/upper bounds present in the constraint.
func rancherVersionMatches(constraint, minor string) bool {
	if constraint == "" || minor == "" {
		return false
	}
	target, ok := parseMinorAsFloat(minor)
	if !ok {
		return false
	}
	// Extract all "X.Y" tokens with their preceding operator.
	lower, upper := 0.0, 0.0
	haveLower, haveUpper := false, false
	tokens := regexp.MustCompile(`(>=|<=|>|<|=)?\s*v?(\d+\.\d+)`).FindAllStringSubmatch(constraint, -1)
	for _, t := range tokens {
		val, ok := parseMinorAsFloat(t[2])
		if !ok {
			continue
		}
		switch t[1] {
		case ">=", ">", "=", "":
			if !haveLower || val < lower {
				lower, haveLower = val, true
			}
		case "<", "<=":
			if !haveUpper || val > upper {
				upper, haveUpper = val, true
			}
		}
	}
	if haveLower && target < lower {
		return false
	}
	if haveUpper && target >= upper {
		return false
	}
	return haveLower || haveUpper
}

// parseMinorAsFloat parses "2.14" → 2.14.
func parseMinorAsFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseRancherMinor extracts "major.minor" from a Rancher version string.
func parseRancherMinor(v string) string {
	m := reRancherMinor.FindStringSubmatch(v)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// parseCPUMillis parses a Kubernetes CPU quantity ("4500m", "2", "1.5") into
// millicores.
func parseCPUMillis(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if strings.HasSuffix(s, "m") {
		n, err := strconv.Atoi(strings.TrimSuffix(s, "m"))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return int(f * 1000), true
}

// parseMemoryMiB parses a Kubernetes memory quantity ("4000Mi", "2Gi", "512Mi",
// "1G", "500M") into MiB.
func parseMemoryMiB(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Check binary suffixes (Gi/Mi/Ki) before decimal (G/M) so the longer match
	// wins.
	switch {
	case strings.HasSuffix(s, "Gi"):
		return scaleMem(strings.TrimSuffix(s, "Gi"), 1024)
	case strings.HasSuffix(s, "Mi"):
		return scaleMem(strings.TrimSuffix(s, "Mi"), 1)
	case strings.HasSuffix(s, "Ki"):
		return scaleMem(strings.TrimSuffix(s, "Ki"), 1.0/1024)
	case strings.HasSuffix(s, "G"):
		// decimal gigabyte → MiB
		return scaleMem(strings.TrimSuffix(s, "G"), 1000*1000*1000/(1024*1024))
	case strings.HasSuffix(s, "M"):
		return scaleMem(strings.TrimSuffix(s, "M"), 1000*1000/(1024*1024))
	default:
		// bare bytes
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		return int(n / (1024 * 1024)), true
	}
}

func scaleMem(num string, mulToMiB float64) (int, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return 0, false
	}
	return int(f * mulToMiB), true
}

// detectChartsRoot resolves the local rancher/charts "charts" directory. It
// honours an explicit configured path (which may point at the repo root or the
// charts/ dir) and otherwise probes common sibling locations of the working
// directory.
func detectChartsRoot(configured string) string {
	var candidates []string
	if configured != "" {
		candidates = append(candidates, configured, filepath.Join(configured, "charts"))
	}
	// Common sibling checkouts.
	candidates = append(candidates,
		"rancher-charts/charts",
		"../rancher-charts/charts",
		"../../rancher-charts/charts",
	)
	for _, c := range candidates {
		if isDir(filepath.Join(c, "rancher-monitoring")) || isChartsDir(c) {
			return c
		}
	}
	return ""
}

// isChartsDir reports whether dir looks like a rancher/charts "charts" dir.
func isChartsDir(dir string) bool {
	return isDir(filepath.Join(dir, "longhorn")) || isDir(filepath.Join(dir, "rancher-monitoring"))
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// ResolveCharts resolves footprints for every chart in the slice using the
// non-LLM tiers, annotating each ChartRequirement in place. It returns the
// names of charts that remain unresolved (candidates for LLM fallback).
func (r *ChartFootprintResolver) ResolveCharts(charts []types.ChartRequirement) (resolved int, unresolved []string) {
	for i := range charts {
		if charts[i].Footprint != nil {
			resolved++
			continue
		}
		if fp, ok := r.Resolve(charts[i].Name); ok {
			charts[i].Footprint = fp
			resolved++
		} else {
			unresolved = append(unresolved, charts[i].Name)
		}
	}
	return resolved, unresolved
}

// String summarises the resolver's source for logging.
func (r *ChartFootprintResolver) String() string {
	src := "catalog-only"
	if r.chartsRoot != "" {
		src = "local:" + r.chartsRoot
	}
	return fmt.Sprintf("chart-footprint resolver [rancher=%s branch=%s source=%s]",
		r.rancherMinor, r.ReleaseBranch(), src)
}
