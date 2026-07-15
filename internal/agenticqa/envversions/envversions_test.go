package envversions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	ghclient "github.com/rancher/tests/internal/agenticqa/github"
)

// fakeGitHub is an in-memory GitHubClient fake for tests.
type fakeGitHub struct {
	tags []string
	// containsCommit maps "base...head" -> whether base contains head.
	containsCommit map[string]bool
	branchHeads    map[string]string
	branches       map[string]bool
	releases       map[string]fakeRelease

	compareCalls []string
}

type fakeRelease struct {
	body   string
	assets []ghclient.ReleaseAsset
}

func (f *fakeGitHub) ListTags(ctx context.Context, owner, repo string) ([]string, error) {
	return f.tags, nil
}

func (f *fakeGitHub) CompareContainsCommit(ctx context.Context, owner, repo, base, head string) (bool, error) {
	key := base + "..." + head
	f.compareCalls = append(f.compareCalls, key)
	return f.containsCommit[key], nil
}

func (f *fakeGitHub) GetBranchHeadSHA(ctx context.Context, owner, repo, branch string) (string, error) {
	sha, ok := f.branchHeads[branch]
	if !ok {
		return "", fmt.Errorf("no such branch %q", branch)
	}
	return sha, nil
}

func (f *fakeGitHub) BranchExists(ctx context.Context, owner, repo, branch string) (bool, error) {
	return f.branches[branch], nil
}

func (f *fakeGitHub) GetReleaseByTag(ctx context.Context, owner, repo, tag string) (string, []ghclient.ReleaseAsset, bool, error) {
	rel, ok := f.releases[tag]
	if !ok {
		return "", nil, false, nil
	}
	return rel.body, rel.assets, true, nil
}

// fakeHTTP serves canned responses keyed by exact URL.
type fakeHTTP struct {
	responses map[string]fakeHTTPResponse
}

type fakeHTTPResponse struct {
	body   []byte
	status int
}

func (f *fakeHTTP) Get(ctx context.Context, url string) ([]byte, int, error) {
	r, ok := f.responses[url]
	if !ok {
		return nil, 404, nil
	}
	return r.body, r.status, nil
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func baseConfig() Config {
	return Config{
		Owner:             "rancher",
		Repo:              "rancher",
		BaseRef:           "release-v2.14",
		MergeCommitSHA:    "abc123def456",
		UpstreamDistro:    "rke2",
		DownstreamDistros: []string{"k3s"},
	}
}

func TestResolve_TagCompare_NewestContainingReleaseWins(t *testing.T) {
	gh := &fakeGitHub{
		tags: []string{"v2.14.2", "v2.14.0", "v2.14.1", "v2.13.9"},
		containsCommit: map[string]bool{
			"v2.14.0...abc123def456": true,
			"v2.14.1...abc123def456": true,
			"v2.14.2...abc123def456": true,
		},
	}
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{}}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	// "Earliest" per spec = the most recent tag that still contains the
	// commit; with all three containing it, the newest (v2.14.2) wins.
	if res.RancherVersion.Value != "v2.14.2" {
		t.Errorf("RancherVersion = %+v, want v2.14.2 (newest containing tag)", res.RancherVersion)
	}
	if res.RancherVersion.Source != SourceTagCompare {
		t.Errorf("RancherVersion.Source = %q, want %q", res.RancherVersion.Source, SourceTagCompare)
	}
	if res.RancherImageTag.Value != "v2.14.2" {
		t.Errorf("RancherImageTag = %+v, want v2.14.2", res.RancherImageTag)
	}
	// v2.13.9 must never be compared: it's outside the 2.14 minor line.
	for _, c := range gh.compareCalls {
		if strings.HasPrefix(c, "v2.13.9") {
			t.Errorf("compare call %q should not have happened (outside minor line)", c)
		}
	}
}

func TestResolve_TagCompare_SkipsNewerTagsNotContainingCommit(t *testing.T) {
	gh := &fakeGitHub{
		tags: []string{"v2.14.2", "v2.14.1", "v2.14.0"},
		containsCommit: map[string]bool{
			"v2.14.2...abc123def456": false, // newest does NOT contain it
			"v2.14.1...abc123def456": true,  // this is the newest that does
			"v2.14.0...abc123def456": true,
		},
	}
	http := &fakeHTTP{}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "v2.14.1" {
		t.Errorf("RancherVersion = %+v, want v2.14.1 (newest tag actually containing the commit)", res.RancherVersion)
	}
}

func TestResolve_PrereleaseFallback_WhenNoFullReleaseContainsCommit(t *testing.T) {
	gh := &fakeGitHub{
		tags: []string{"v2.14.0-rc1", "v2.14.0-rc2", "v2.14.0"},
		containsCommit: map[string]bool{
			"v2.14.0...abc123def456":     false, // full release does NOT contain it
			"v2.14.0-rc2...abc123def456": true,  // newest prerelease that does
			"v2.14.0-rc1...abc123def456": true,
		},
	}
	http := &fakeHTTP{}
	cfg := baseConfig() // IncludePrereleases stays false

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "v2.14.0-rc2" {
		t.Errorf("RancherVersion = %+v, want v2.14.0-rc2 (latest prerelease containing the change)", res.RancherVersion)
	}
	if res.RancherVersion.Source != SourceTagComparePrerel {
		t.Errorf("RancherVersion.Source = %q, want %q", res.RancherVersion.Source, SourceTagComparePrerel)
	}
}

func TestResolve_FullReleaseBeatsNewerPrerelease(t *testing.T) {
	// A newer-numbered prerelease AND an older full release both contain the
	// commit; the full release must win regardless of --include-prereleases.
	gh := &fakeGitHub{
		tags: []string{"v2.14.1-rc5", "v2.14.0"},
		containsCommit: map[string]bool{
			"v2.14.0...abc123def456":     true,
			"v2.14.1-rc5...abc123def456": true,
		},
	}
	http := &fakeHTTP{}

	cfg := baseConfig()
	cfg.IncludePrereleases = true
	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "v2.14.0" {
		t.Errorf("RancherVersion = %+v, want v2.14.0 (full release beats any prerelease)", res.RancherVersion)
	}
	if res.RancherVersion.Source != SourceTagCompare {
		t.Errorf("RancherVersion.Source = %q, want %q", res.RancherVersion.Source, SourceTagCompare)
	}
}

func TestResolve_DevFallback_WhenNoTagContainsCommit(t *testing.T) {
	gh := &fakeGitHub{
		tags: []string{"v2.14.0"},
		containsCommit: map[string]bool{
			"v2.14.0...abc123def456": false,
		},
		branchHeads: map[string]string{"release-v2.14": "deadbeef00112233"},
	}
	http := &fakeHTTP{}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "dev-v2.14" {
		t.Errorf("RancherVersion = %+v, want dev-v2.14", res.RancherVersion)
	}
	if res.RancherVersion.Source != SourceInDevelopment {
		t.Errorf("RancherVersion.Source = %q, want %q", res.RancherVersion.Source, SourceInDevelopment)
	}
	// release-v2.14 base ref -> CI release-branch tag "<X.Y>-<full-sha>-head".
	if res.RancherImageTag.Value != "2.14-deadbeef00112233-head" {
		t.Errorf("RancherImageTag = %+v, want 2.14-deadbeef00112233-head", res.RancherImageTag)
	}
}

func TestResolve_DevFallback_MainBranchUsesResolvedMinorHeadTag(t *testing.T) {
	// PR against main: no dev-v branch on rancher/rancher, minor comes from
	// package/Dockerfile; image tag is "v<minor>-<full-sha>-head".
	fullSHA := "8be6998d2bbf1225b9d74e495218e4e8ca15c125"
	gh := &fakeGitHub{
		tags:           []string{}, // no tags contain a main-only commit
		branchHeads:    map[string]string{"main": fullSHA},
		containsCommit: map[string]bool{},
	}
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		"https://raw.githubusercontent.com/rancher/rancher/mc123/package/Dockerfile": {
			body:   []byte("ARG CHART_DEFAULT_BRANCH=dev-v2.15\nARG CATTLE_KDM_BRANCH=dev-v2.15\n"),
			status: 200,
		},
	}}
	cfg := baseConfig()
	cfg.BaseRef = "main"
	cfg.MergeCommitSHA = "mc123"

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherMinor != "2.15" {
		t.Errorf("RancherMinor = %q, want 2.15 (from package/Dockerfile)", res.RancherMinor)
	}
	if res.RancherVersion.Value != "dev-v2.15" {
		t.Errorf("RancherVersion = %+v, want dev-v2.15", res.RancherVersion)
	}
	want := "v2.15-" + fullSHA + "-head"
	if res.RancherImageTag.Value != want {
		t.Errorf("RancherImageTag = %+v, want %q", res.RancherImageTag, want)
	}
}

func TestResolve_DevFallback_WhenUnmerged(t *testing.T) {
	gh := &fakeGitHub{
		branchHeads: map[string]string{"master": "cafef00d"},
	}
	http := &fakeHTTP{}
	cfg := baseConfig()
	cfg.BaseRef = "master"
	cfg.MergeCommitSHA = "" // unmerged PR

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "master" {
		t.Errorf("RancherVersion = %+v, want %q (no minor parseable from base ref)", res.RancherVersion, "master")
	}
	if res.RancherVersion.Source != SourceInDevelopment {
		t.Errorf("RancherVersion.Source = %q, want %q", res.RancherVersion.Source, SourceInDevelopment)
	}
	if res.RancherImageTag.Value != "cafef00d" {
		t.Errorf("RancherImageTag = %+v, want cafef00d", res.RancherImageTag)
	}
	if len(gh.tags) != 0 && len(gh.compareCalls) != 0 {
		t.Errorf("no tag comparisons should have happened for an unmerged PR")
	}
}

func TestResolve_Placeholder_WhenBranchHeadLookupFails(t *testing.T) {
	gh := &fakeGitHub{} // no tags, no branch heads registered
	http := &fakeHTTP{}
	cfg := baseConfig()

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Source != SourcePlaceholder {
		t.Errorf("RancherVersion.Source = %q, want %q", res.RancherVersion.Source, SourcePlaceholder)
	}
	if res.RancherVersion.Value != "${RANCHER_VERSION}" {
		t.Errorf("RancherVersion.Value = %q, want ${RANCHER_VERSION}", res.RancherVersion.Value)
	}
	if res.RancherImageTag.Value != "${RANCHER_IMAGE_TAG}" {
		t.Errorf("RancherImageTag.Value = %q, want ${RANCHER_IMAGE_TAG}", res.RancherImageTag.Value)
	}
}

func TestResolve_Override_WinsOverEverything(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.14.0"},
		containsCommit: map[string]bool{"v2.14.0...abc123def456": true},
	}
	http := &fakeHTTP{}
	cfg := baseConfig()
	cfg.Overrides = map[string]string{
		EnvRancherVersion:       "v2.14.99-pinned",
		EnvVarForDistro("rke2"): "v1.99.0+rke2r1",
	}

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "v2.14.99-pinned" || res.RancherVersion.Source != SourceOverride {
		t.Errorf("RancherVersion = %+v, want override v2.14.99-pinned", res.RancherVersion)
	}
	if res.KubernetesVersionByDistro["rke2"].Value != "v1.99.0+rke2r1" || res.KubernetesVersionByDistro["rke2"].Source != SourceOverride {
		t.Errorf("rke2 version = %+v, want override", res.KubernetesVersionByDistro["rke2"])
	}
}

func TestResolve_KDM_PicksNewestUnderChartConstraint(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.14.0"},
		containsCommit: map[string]bool{"v2.14.0...abc123def456": true},
	}
	kdm := kdmDocument{
		RKE2: kdmChannel{Releases: []kdmRelease{
			{Version: "v1.30.5+rke2r1"},
			{Version: "v1.31.5+rke2r1"},
			{Version: "v1.35.0+rke2r1"},     // excluded by kubeVersion constraint
			{Version: "v1.32.0-rc1+rke2r1"}, // excluded: prerelease
		}},
		K3s: kdmChannel{Releases: []kdmRelease{
			{Version: "v1.30.5+k3s1"},
		}},
	}
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		"https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/release-v2.14/data/data.json": {
			body: mustJSON(t, kdm), status: 200,
		},
		"https://raw.githubusercontent.com/rancher/rancher/v2.14.0/chart/Chart.yaml": {
			body:   []byte("apiVersion: v2\nkubeVersion: \"< 1.35.0-0\"\nname: rancher\n"),
			status: 200,
		},
	}}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	rke2 := res.KubernetesVersionByDistro["rke2"]
	if rke2.Value != "v1.31.5+rke2r1" {
		t.Errorf("rke2 version = %+v, want v1.31.5+rke2r1 (newest under constraint, excluding prerelease and out-of-bound)", rke2)
	}
	if rke2.Source != SourceKDM {
		t.Errorf("rke2 source = %q, want %q", rke2.Source, SourceKDM)
	}

	k3s := res.KubernetesVersionByDistro["k3s"]
	if k3s.Value != "v1.30.5+k3s1" {
		t.Errorf("k3s version = %+v, want v1.30.5+k3s1", k3s)
	}
}

func TestResolve_KDM_PrefersAppDefaultsLine(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.15.0"},
		containsCommit: map[string]bool{"v2.15.0...abc123def456": true},
	}
	appDefaults := []kdmAppDefaults{{
		AppName: "rancher",
		Defaults: []kdmAppDefaultsRow{
			{AppVersion: ">= 2.14.0-0 < 2.15.100-0", DefaultVersion: "1.35.x"},
			{AppVersion: ">= 2.15.0-0 < 2.16.100-0", DefaultVersion: "1.36.x"},
		},
	}}
	kdm := kdmDocument{
		RKE2: kdmChannel{
			AppDefaults: appDefaults,
			Releases: []kdmRelease{
				{Version: "v1.35.6+rke2r1"},
				{Version: "v1.36.1+rke2r1"},
				{Version: "v1.36.2+rke2r1"}, // newest on the 1.36 default line
			},
		},
		K3s: kdmChannel{
			AppDefaults: appDefaults,
			Releases: []kdmRelease{
				{Version: "v1.35.6+k3s1"},
				{Version: "v1.36.2+k3s1"},
			},
		},
	}
	cfg := baseConfig()
	cfg.BaseRef = "release-v2.15"
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		"https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/release-v2.15/data/data.json": {
			body: mustJSON(t, kdm), status: 200,
		},
	}}

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	rke2 := res.KubernetesVersionByDistro["rke2"]
	if rke2.Value != "v1.36.2+rke2r1" {
		t.Errorf("rke2 version = %+v, want v1.36.2+rke2r1 (newest on KDM appDefaults 1.36 line for Rancher 2.15)", rke2)
	}
	k3s := res.KubernetesVersionByDistro["k3s"]
	if k3s.Value != "v1.36.2+k3s1" {
		t.Errorf("k3s version = %+v, want v1.36.2+k3s1", k3s)
	}
}

func TestResolve_KDM_ReleasesFallbackURL(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.15.0"},
		containsCommit: map[string]bool{"v2.15.0...abc123def456": true},
	}
	kdm := kdmDocument{
		RKE2: kdmChannel{Releases: []kdmRelease{{Version: "v1.36.2+rke2r1"}}},
		K3s:  kdmChannel{Releases: []kdmRelease{{Version: "v1.36.2+k3s1"}}},
	}
	cfg := baseConfig()
	cfg.BaseRef = "release-v2.15"
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		// raw layout deliberately absent; only the releases.rancher.com layout responds.
		"https://releases.rancher.com/kontainer-driver-metadata/release-v2.15/data.json": {
			body: mustJSON(t, kdm), status: 200,
		},
	}}

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if got := res.KubernetesVersionByDistro["rke2"].Value; got != "v1.36.2+rke2r1" {
		t.Errorf("rke2 version = %q, want v1.36.2+rke2r1 (via releases.rancher.com fallback)", got)
	}
}

func TestCandidateKDMBranches(t *testing.T) {
	r := NewResolver(&fakeGitHub{}, &fakeHTTP{}, baseConfig())

	tests := []struct {
		name           string
		minor          string
		rancherVersion string
		rancherSource  string
		want           []string
	}{
		{"in-development", "2.15", "dev-v2.15", SourceInDevelopment, []string{"dev-v2.15", "release-v2.15"}},
		{"prerelease source", "2.15", "v2.15.0-alpha18", SourceTagComparePrerel, []string{"dev-v2.15", "release-v2.15"}},
		{"prerelease value with tag-compare source", "2.15", "v2.15.0-rc3", SourceTagCompare, []string{"dev-v2.15", "release-v2.15"}},
		{"full release", "2.14", "v2.14.3", SourceTagCompare, []string{"release-v2.14", "dev-v2.14"}},
		{"unknown minor", "", "", SourceInDevelopment, []string{"master"}},
	}
	for _, tc := range tests {
		got := r.candidateKDMBranches(tc.minor, tc.rancherVersion, tc.rancherSource)
		assertStringSlice(t, got, tc.want)
	}
}

func TestResolve_KDM_PrereleaseUsesDevBranch(t *testing.T) {
	// Reproduces the reported bug: a prerelease Rancher version must query the
	// dev- KDM branch (release-vX.Y does not exist until GA).
	gh := &fakeGitHub{
		tags:           []string{"v2.15.0-alpha18"},
		containsCommit: map[string]bool{"v2.15.0-alpha18...abc123def456": true},
	}
	kdm := kdmDocument{
		RKE2: kdmChannel{Releases: []kdmRelease{{Version: "v1.36.2+rke2r1"}}},
		K3s:  kdmChannel{Releases: []kdmRelease{{Version: "v1.36.2+k3s1"}}},
	}
	cfg := baseConfig()
	cfg.BaseRef = "main"
	cfg.IncludePrereleases = true
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		// package/Dockerfile supplies the minor for a main-targeted PR.
		"https://raw.githubusercontent.com/rancher/rancher/abc123def456/package/Dockerfile": {
			body: []byte("ARG CATTLE_KDM_BRANCH=dev-v2.15\n"), status: 200,
		},
		// Only the dev- branch exists; release-v2.15 is deliberately absent.
		"https://raw.githubusercontent.com/rancher/kontainer-driver-metadata/dev-v2.15/data/data.json": {
			body: mustJSON(t, kdm), status: 200,
		},
	}}

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "v2.15.0-alpha18" {
		t.Fatalf("RancherVersion = %+v, want v2.15.0-alpha18", res.RancherVersion)
	}
	rke2 := res.KubernetesVersionByDistro["rke2"]
	if rke2.Value != "v1.36.2+rke2r1" || rke2.Source != SourceKDM {
		t.Errorf("rke2 = %+v, want v1.36.2+rke2r1 (kdm, from dev-v2.15)", rke2)
	}
	k3s := res.KubernetesVersionByDistro["k3s"]
	if k3s.Value != "v1.36.2+k3s1" || k3s.Source != SourceKDM {
		t.Errorf("k3s = %+v, want v1.36.2+k3s1 (kdm, from dev-v2.15)", k3s)
	}
	// Alpha builds are only published to the "rancher-alpha" Helm repo, not
	// the qa-infra-automation default "rancher-latest".
	if res.RancherChartRepoName != "rancher-alpha" {
		t.Errorf("RancherChartRepoName = %q, want rancher-alpha", res.RancherChartRepoName)
	}
	if res.RancherChartRepoURL != "https://releases.rancher.com/server-charts/alpha" {
		t.Errorf("RancherChartRepoURL = %q, want the alpha channel URL", res.RancherChartRepoURL)
	}
}

func TestChartRepoForVersion(t *testing.T) {
	tests := []struct {
		version  string
		wantName string
		wantURL  string
	}{
		{"v2.15.0-alpha19", "rancher-alpha", "https://releases.rancher.com/server-charts/alpha"},
		{"2.15.0-alpha1", "rancher-alpha", "https://releases.rancher.com/server-charts/alpha"},
		{"v2.15.0-rc3", "", ""}, // rc is already in rancher-latest
		{"v2.15.0", "", ""},     // GA is already in rancher-latest
		{"dev-v2.15", "", ""},   // dev label, no chart published under this name
		{"", "", ""},
	}
	for _, tc := range tests {
		name, url := chartRepoForVersion(tc.version)
		if name != tc.wantName || url != tc.wantURL {
			t.Errorf("chartRepoForVersion(%q) = (%q, %q), want (%q, %q)", tc.version, name, url, tc.wantName, tc.wantURL)
		}
	}
}

func TestResolve_ChartRepo_DefaultsToEmptyForFullRelease(t *testing.T) {
	// A GA/rc resolution must NOT set a chart repo override; the
	// qa-infra-automation default (rancher-latest) already carries them.
	gh := &fakeGitHub{
		tags:           []string{"v2.14.3"},
		containsCommit: map[string]bool{"v2.14.3...abc123def456": true},
	}
	http := &fakeHTTP{}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	if res.RancherChartRepoName != "" || res.RancherChartRepoURL != "" {
		t.Errorf("expected no chart repo override for a full release, got name=%q url=%q", res.RancherChartRepoName, res.RancherChartRepoURL)
	}
}

func TestResolve_ChartRepo_FollowsOverriddenVersion(t *testing.T) {
	// When RANCHER_VERSION is pinned via override to an alpha build, the
	// chart repo override must still be computed from that final value.
	gh := &fakeGitHub{
		tags:           []string{"v2.14.3"},
		containsCommit: map[string]bool{"v2.14.3...abc123def456": true},
	}
	http := &fakeHTTP{}
	cfg := baseConfig()
	cfg.Overrides = map[string]string{EnvRancherVersion: "v2.15.0-alpha19"}

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.RancherVersion.Value != "v2.15.0-alpha19" {
		t.Fatalf("RancherVersion = %+v, want the override value", res.RancherVersion)
	}
	if res.RancherChartRepoName != "rancher-alpha" {
		t.Errorf("RancherChartRepoName = %q, want rancher-alpha (derived from the overridden version)", res.RancherChartRepoName)
	}
}

func TestResolve_KDM_FallsBackFromReleaseToDevBranch(t *testing.T) {
	// Full release resolution tries release- first; when that 404s the dev-
	// branch is used as a bidirectional fallback.
	gh := &fakeGitHub{
		tags:           []string{"v2.15.0"},
		containsCommit: map[string]bool{"v2.15.0...abc123def456": true},
	}
	kdm := kdmDocument{
		RKE2: kdmChannel{Releases: []kdmRelease{{Version: "v1.36.2+rke2r1"}}},
		K3s:  kdmChannel{Releases: []kdmRelease{{Version: "v1.36.2+k3s1"}}},
	}
	cfg := baseConfig()
	cfg.BaseRef = "release-v2.15"
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		// release-v2.15 absent on both hosts; only dev-v2.15 responds.
		"https://releases.rancher.com/kontainer-driver-metadata/dev-v2.15/data.json": {
			body: mustJSON(t, kdm), status: 200,
		},
	}}

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if got := res.KubernetesVersionByDistro["rke2"].Value; got != "v1.36.2+rke2r1" {
		t.Errorf("rke2 version = %q, want v1.36.2+rke2r1 (release->dev bidirectional fallback)", got)
	}
}

func TestResolve_KDM_Placeholder_WhenFetchFails(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.14.0"},
		containsCommit: map[string]bool{"v2.14.0...abc123def456": true},
	}
	http := &fakeHTTP{} // no responses registered -> 404 for everything

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	rke2 := res.KubernetesVersionByDistro["rke2"]
	if rke2.Source != SourcePlaceholder {
		t.Errorf("rke2 source = %q, want %q", rke2.Source, SourcePlaceholder)
	}
	if rke2.Value != "${RKE2_VERSION}" {
		t.Errorf("rke2 value = %q, want ${RKE2_VERSION}", rke2.Value)
	}
}

func TestResolve_CertManager_ReleaseNotesTier(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.14.0"},
		containsCommit: map[string]bool{"v2.14.0...abc123def456": true},
		releases: map[string]fakeRelease{
			"v2.14.0": {body: "This release requires cert-manager v1.14.5 or later."},
		},
	}
	http := &fakeHTTP{}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	if res.CertManagerVersion.Value != "1.14.5" {
		t.Errorf("CertManagerVersion = %+v, want 1.14.5", res.CertManagerVersion)
	}
	if res.CertManagerVersion.Source != SourceReleaseNotes {
		t.Errorf("CertManagerVersion.Source = %q, want %q", res.CertManagerVersion.Source, SourceReleaseNotes)
	}
}

func TestResolve_CertManager_ImagesTxtTier(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.14.0"},
		containsCommit: map[string]bool{"v2.14.0...abc123def456": true},
		releases: map[string]fakeRelease{
			"v2.14.0": {
				body: "No cert-manager mention here.",
				assets: []ghclient.ReleaseAsset{
					{Name: "rancher-images.txt", BrowserDownloadURL: "https://example.com/rancher-images.txt"},
				},
			},
		},
	}
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		"https://example.com/rancher-images.txt": {
			body:   []byte("rancher/mirrored-cert-manager-controller:v1.14.5\nrancher/mirrored-cert-manager-cainjector:v1.14.5\n"),
			status: 200,
		},
	}}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	if res.CertManagerVersion.Value != "1.14.5" {
		t.Errorf("CertManagerVersion = %+v, want 1.14.5", res.CertManagerVersion)
	}
	if res.CertManagerVersion.Source != SourceImagesTxt {
		t.Errorf("CertManagerVersion.Source = %q, want %q", res.CertManagerVersion.Source, SourceImagesTxt)
	}
}

func TestResolve_CertManager_UpstreamLatestFallback(t *testing.T) {
	gh := &fakeGitHub{
		tags:           []string{"v2.14.0"},
		containsCommit: map[string]bool{"v2.14.0...abc123def456": true},
		// No release registered at all -> ok=false from GetReleaseByTag.
	}
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		"https://api.github.com/repos/cert-manager/cert-manager/releases": {
			body: mustJSON(t, []certManagerRelease{
				{TagName: "v1.15.0-rc1", Prerelease: true},
				{TagName: "v1.14.7", Prerelease: false},
				{TagName: "v1.14.6", Prerelease: false},
			}),
			status: 200,
		},
	}}

	r := NewResolver(gh, http, baseConfig())
	res := r.Resolve(context.Background())

	// The leading "v" must be stripped so this tier's output format matches
	// the release-notes/images-txt tiers (bare "X.Y.Z"), which is what
	// qa-infra-automation's rancher vars.yaml (cert_manager_version) expects.
	if res.CertManagerVersion.Value != "1.14.7" {
		t.Errorf("CertManagerVersion = %+v, want 1.14.7 (newest non-prerelease, no leading v)", res.CertManagerVersion)
	}
	if res.CertManagerVersion.Source != SourceUpstreamLatest {
		t.Errorf("CertManagerVersion.Source = %q, want %q", res.CertManagerVersion.Source, SourceUpstreamLatest)
	}
}

func TestResolve_CertManager_NotAttemptedWhenInDevelopment(t *testing.T) {
	gh := &fakeGitHub{
		branchHeads: map[string]string{"release-v2.14": "deadbeef"},
		// releases map deliberately left populated to prove it's not consulted.
		releases: map[string]fakeRelease{
			"dev-v2.14": {body: "cert-manager v9.9.9"},
		},
	}
	http := &fakeHTTP{}
	cfg := baseConfig()
	cfg.MergeCommitSHA = "" // force in-development fallback

	r := NewResolver(gh, http, cfg)
	res := r.Resolve(context.Background())

	if res.CertManagerVersion.Source == SourceReleaseNotes {
		t.Errorf("cert-manager should not resolve from release notes while in-development, got %+v", res.CertManagerVersion)
	}
}

func TestPartitionCandidateTags(t *testing.T) {
	tags := []string{"v2.14.2", "v2.14.0", "v2.14.1", "v2.13.9", "v2.14.0-rc1", "v2.14.0-rc10", "not-a-tag", "v3.0.0"}

	releases, prereleases := partitionCandidateTags(tags, "2.14")
	// Releases newest-first.
	assertStringSlice(t, releases, []string{"v2.14.2", "v2.14.1", "v2.14.0"})
	// Prereleases newest-first with numeric ordering (rc10 > rc1).
	assertStringSlice(t, prereleases, []string{"v2.14.0-rc10", "v2.14.0-rc1"})

	// No minor known: every semver-shaped tag is a candidate, newest-first.
	releases, prereleases = partitionCandidateTags(tags, "")
	assertStringSlice(t, releases, []string{"v3.0.0", "v2.14.2", "v2.14.1", "v2.14.0", "v2.13.9"})
	assertStringSlice(t, prereleases, []string{"v2.14.0-rc10", "v2.14.0-rc1"})
}

func TestResolveMinor_DockerfilePreferredOverBaseRef(t *testing.T) {
	gh := &fakeGitHub{}
	http := &fakeHTTP{responses: map[string]fakeHTTPResponse{
		"https://raw.githubusercontent.com/rancher/rancher/mc999/package/Dockerfile": {
			body:   []byte("ARG CATTLE_KDM_BRANCH=dev-v2.15\n"),
			status: 200,
		},
	}}
	cfg := baseConfig()
	cfg.BaseRef = "release-v2.14" // would parse to 2.14
	cfg.MergeCommitSHA = "mc999"

	r := NewResolver(gh, http, cfg)
	minor, source := r.resolveMinor(context.Background())
	if minor != "2.15" || source != SourceDockerfileMinor {
		t.Errorf("resolveMinor = (%q, %q), want (2.15, %q)", minor, source, SourceDockerfileMinor)
	}
}

func TestResolveMinor_FallsBackToBaseRefThenDefault(t *testing.T) {
	gh := &fakeGitHub{}
	http := &fakeHTTP{} // no Dockerfile

	cfg := baseConfig()
	cfg.BaseRef = "release-v2.14"
	cfg.MergeCommitSHA = ""
	r := NewResolver(gh, http, cfg)
	if minor, source := r.resolveMinor(context.Background()); minor != "2.14" || source != SourceBaseRefMinor {
		t.Errorf("resolveMinor = (%q, %q), want (2.14, %q)", minor, source, SourceBaseRefMinor)
	}

	cfg.BaseRef = "main"
	cfg.DefaultMinor = "2.99"
	r2 := NewResolver(gh, http, cfg)
	if minor, source := r2.resolveMinor(context.Background()); minor != "2.99" || source != SourceConfigDefaultMinor {
		t.Errorf("resolveMinor = (%q, %q), want (2.99, %q)", minor, source, SourceConfigDefaultMinor)
	}
}

func TestHeadImageTag(t *testing.T) {
	tests := []struct {
		branch, minor, sha, want string
	}{
		{"main", "2.15", "abc", "v2.15-abc-head"},
		{"release/v2.14", "2.14", "abc", "2.14-abc-head"},
		{"release-v2.14", "2.14", "abc", "2.14-abc-head"},
		{"main", "", "abc", "abc"},
	}
	for _, tc := range tests {
		if got := headImageTag(tc.branch, tc.minor, tc.sha); got != tc.want {
			t.Errorf("headImageTag(%q,%q,%q) = %q, want %q", tc.branch, tc.minor, tc.sha, got, tc.want)
		}
	}
}

func TestStripLeadingV(t *testing.T) {
	tests := []struct{ in, want string }{
		{"v1.21.0", "1.21.0"},
		{"V1.21.0", "1.21.0"},
		{"1.21.0", "1.21.0"},
		{"", ""},
		{"v", ""},
	}
	for _, tc := range tests {
		if got := stripLeadingV(tc.in); got != tc.want {
			t.Errorf("stripLeadingV(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseBaseRefMinor(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"release-v2.14", "2.14"},
		{"dev-v2.14", "2.14"},
		{"v2.14", "2.14"},
		{"master", ""},
		{"main", ""},
	}
	for _, tc := range tests {
		if got := parseBaseRefMinor(tc.in); got != tc.want {
			t.Errorf("parseBaseRefMinor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
