package envconfig

import "testing"

func TestSelectInstanceTypeForSpec_PrefersCheapestFamily(t *testing.T) {
	tmpl := Generate().CattleConfig

	// No preference: cheapest overall meeting 2 vCPU / 8 GiB is t3a.large
	// (CostRank 11), not m5.large.
	it, ok := tmpl.SelectInstanceTypeForSpec("aws", 2, 8, nil)
	if !ok {
		t.Fatal("expected a selection")
	}
	if it.Name != "t3a.large" {
		t.Errorf("no preference: want t3a.large (cheapest), got %s", it.Name)
	}
}

func TestSelectInstanceTypeForSpec_CostConsciousFamilies(t *testing.T) {
	tmpl := Generate().CattleConfig

	// Cost-conscious profile prefers t3a, then t3.
	it, ok := tmpl.SelectInstanceTypeForSpec("aws", 4, 16, []string{"t3a", "t3"})
	if !ok {
		t.Fatal("expected a selection")
	}
	if it.Name != "t3a.xlarge" {
		t.Errorf("want t3a.xlarge, got %s", it.Name)
	}
}

func TestSelectInstanceTypeForSpec_PerformanceFamilies(t *testing.T) {
	tmpl := Generate().CattleConfig

	// Performance profile prefers m5/c5/r5; for 4 vCPU / 16 GiB, m5.xlarge.
	it, ok := tmpl.SelectInstanceTypeForSpec("aws", 4, 16, []string{"m5", "c5", "r5"})
	if !ok {
		t.Fatal("expected a selection")
	}
	if it.Name != "m5.xlarge" {
		t.Errorf("want m5.xlarge, got %s", it.Name)
	}
}

func TestSelectInstanceTypeForSpec_FallsBackWhenFamilyMissing(t *testing.T) {
	tmpl := Generate().CattleConfig

	// A memory-heavy spec (8 vCPU / 64 GiB) has no fit in t3a/t3 (max 32 GiB),
	// so selection falls back to any family — r5.2xlarge (64 GiB).
	it, ok := tmpl.SelectInstanceTypeForSpec("aws", 8, 64, []string{"t3a", "t3"})
	if !ok {
		t.Fatal("expected a fallback selection")
	}
	if it.MemoryGiB < 64 {
		t.Errorf("fallback should meet 64 GiB, got %s (%d GiB)", it.Name, it.MemoryGiB)
	}
}

func TestSelectInstanceTypeForSpec_NoFit(t *testing.T) {
	tmpl := Generate().CattleConfig
	if _, ok := tmpl.SelectInstanceTypeForSpec("aws", 999, 999, nil); ok {
		t.Error("expected no fit for an impossible spec")
	}
	if _, ok := tmpl.SelectInstanceTypeForSpec("nope", 1, 1, nil); ok {
		t.Error("expected no selection for unknown provider")
	}
}

func TestPerformanceProfileDefined(t *testing.T) {
	sp := GenerateSizingPolicy()
	prof, name, ok := sp.ResolveProfile("performance")
	if !ok {
		t.Fatal("performance profile should be defined")
	}
	if name != "performance" {
		t.Errorf("want name performance, got %s", name)
	}
	if got := prof.Downstream.PreferredFamilies; len(got) == 0 || got[0] != "m5" {
		t.Errorf("performance downstream should prefer m5 first, got %v", got)
	}
	if prof.Downstream.MaxNodeVCPUs != 16 {
		t.Errorf("performance should allow 16 vCPU nodes, got %d", prof.Downstream.MaxNodeVCPUs)
	}
}

func TestDefaultProfilesAreCostConscious(t *testing.T) {
	sp := GenerateSizingPolicy()
	for _, name := range []string{"minimal", "balanced", "ha"} {
		prof, _, ok := sp.ResolveProfile(name)
		if !ok {
			t.Fatalf("%s profile missing", name)
		}
		fams := prof.Downstream.PreferredFamilies
		if len(fams) == 0 || fams[0] != "t3a" {
			t.Errorf("%s should prefer t3a first, got %v", name, fams)
		}
	}
}

func TestChartSizingDefaults(t *testing.T) {
	cs := GenerateChartSizing()
	if cs.IsEmpty() {
		t.Fatal("default chart sizing should not be empty")
	}
	if _, ok := cs.Catalog["longhorn"]; !ok {
		t.Error("catalog should include longhorn fallback")
	}
	if cs.Source.DefaultRancherMinor == "" {
		t.Error("source should carry a default rancher minor")
	}
}
