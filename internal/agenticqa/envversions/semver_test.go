package envversions

import "testing"

func TestParseSemver(t *testing.T) {
	tests := []struct {
		in     string
		wantOK bool
		major  int
		minor  int
		patch  int
		pre    string
	}{
		{"v2.14.3", true, 2, 14, 3, ""},
		{"2.14.3", true, 2, 14, 3, ""},
		{"v2.14.0-rc1", true, 2, 14, 0, "rc1"},
		{"v1.30.5+k3s1", true, 1, 30, 5, ""},
		{"v1.31.0-rc1+k3s1", true, 1, 31, 0, "rc1"},
		{"not-a-version", false, 0, 0, 0, ""},
		{"${RANCHER_VERSION}", false, 0, 0, 0, ""},
	}
	for _, tc := range tests {
		sv, ok := parseSemver(tc.in)
		if ok != tc.wantOK {
			t.Errorf("parseSemver(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if sv.major != tc.major || sv.minor != tc.minor || sv.patch != tc.patch || sv.pre != tc.pre {
			t.Errorf("parseSemver(%q) = %+v, want major=%d minor=%d patch=%d pre=%q", tc.in, sv, tc.major, tc.minor, tc.patch, tc.pre)
		}
	}
}

func TestSemverLess(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"v2.14.0", "v2.14.1", true},
		{"v2.14.1", "v2.14.0", false},
		{"v2.14.0-rc1", "v2.14.0", true},
		{"v2.14.0", "v2.14.0-rc1", false},
		{"v2.14.0-rc1", "v2.14.0-rc2", true},
		{"v2.9.0", "v2.14.0", true},
		{"v2.14.0", "v2.14.0", false},
		// Numeric-aware prerelease ordering: higher # is greater/more recent.
		{"v2.14.0-rc2", "v2.14.0-rc10", true},
		{"v2.14.0-rc10", "v2.14.0-rc2", false},
		{"v2.15.0-alpha2", "v2.15.0-alpha10", true},
		{"v2.15.0-rc1", "v2.15.0-rc1", false},
	}
	for _, tc := range tests {
		a, _ := parseSemver(tc.a)
		b, _ := parseSemver(tc.b)
		if got := a.less(b); got != tc.want {
			t.Errorf("(%q).less(%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSortSemverAsc(t *testing.T) {
	in := []string{"v2.14.10", "v2.14.2", "v2.14.1", "not-a-version", "v2.14.0-rc1"}
	sorted := sortSemverAsc(in)
	var raws []string
	for _, sv := range sorted {
		raws = append(raws, sv.raw)
	}
	want := []string{"v2.14.0-rc1", "v2.14.1", "v2.14.2", "v2.14.10"}
	if len(raws) != len(want) {
		t.Fatalf("sortSemverAsc = %v, want %v", raws, want)
	}
	for i := range want {
		if raws[i] != want[i] {
			t.Errorf("sortSemverAsc[%d] = %q, want %q (full: %v)", i, raws[i], want[i], raws)
		}
	}
}

func TestSplitPrerelease(t *testing.T) {
	tests := []struct {
		in    string
		label string
		num   int
	}{
		{"rc10", "rc", 10},
		{"rc2", "rc", 2},
		{"alpha", "alpha", -1},
		{"rc.3", "rc.", 3},
		{"beta0", "beta", 0},
	}
	for _, tc := range tests {
		label, num := splitPrerelease(tc.in)
		if label != tc.label || num != tc.num {
			t.Errorf("splitPrerelease(%q) = (%q, %d), want (%q, %d)", tc.in, label, num, tc.label, tc.num)
		}
	}
}

func TestSortSemverAsc_FullReleaseOutranksNewerNumberedPrerelease(t *testing.T) {
	in := []string{"v2.15.0-rc10", "v2.15.0", "v2.15.0-rc2"}
	sorted := sortSemverAsc(in)
	var raws []string
	for _, sv := range sorted {
		raws = append(raws, sv.raw)
	}
	want := []string{"v2.15.0-rc2", "v2.15.0-rc10", "v2.15.0"}
	if len(raws) != len(want) {
		t.Fatalf("sortSemverAsc = %v, want %v", raws, want)
	}
	for i := range want {
		if raws[i] != want[i] {
			t.Errorf("sortSemverAsc[%d] = %q, want %q (full: %v)", i, raws[i], want[i], raws)
		}
	}
}

func TestIsPrerelease(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"v2.14.0", false},
		{"v2.14.0-rc1", true},
		{"v2.14.0-alpha1", true},
		{"v1.31.0-beta.0+k3s1", true},
		{"v1.30.5+k3s1", false},
	}
	for _, tc := range tests {
		if got := isPrerelease(tc.in); got != tc.want {
			t.Errorf("isPrerelease(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseMaxKubeVersionConstraint(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"< 1.35.0-0", "1.35.0-0", true},
		{">= 1.28.0 < 1.35.0-0", "1.35.0-0", true},
		{"&gt;= 1.28.0 &lt; 1.35.0-0", "1.35.0-0", true},
		{">= 1.28.0", "", false},
		{"", "", false},
	}
	for _, tc := range tests {
		got, ok := parseMaxKubeVersionConstraint(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("parseMaxKubeVersionConstraint(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestUnderMaxKubeVersion(t *testing.T) {
	tests := []struct {
		v, max string
		want   bool
	}{
		{"1.30.5+k3s1", "1.35.0-0", true},
		{"1.35.0+k3s1", "1.35.0-0", false}, // release 1.35.0 is NOT < 1.35.0-0 (prerelease sorts lower)
		{"1.35.1+k3s1", "1.35.0-0", false},
		{"1.34.9+k3s1", "1.35.0-0", true},
	}
	for _, tc := range tests {
		if got := underMaxKubeVersion(tc.v, tc.max); got != tc.want {
			t.Errorf("underMaxKubeVersion(%q, %q) = %v, want %v", tc.v, tc.max, got, tc.want)
		}
	}
}
