// Package envversions best-effort resolves environment versions.
// Failed resolution retains placeholders.
package envversions

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/sirupsen/logrus"

	ghclient "github.com/rancher/tests/internal/agenticqa/github"
	"github.com/rancher/tests/internal/agenticqa/types"
)

// Source tier labels recorded in types.ResolvedVersion.Source.
const (
	SourceOverride           = "override"
	SourceTagCompare         = "tag-compare"
	SourceTagComparePrerel   = "tag-compare-prerelease"
	SourceKDM                = "kdm"
	SourceReleaseNotes       = "release-notes"
	SourceImagesTxt          = "images-txt"
	SourceUpstreamLatest     = "upstream-latest"
	SourceInDevelopment      = "in-development"
	SourcePlaceholder        = "placeholder"
	SourceDockerfileMinor    = "dockerfile-minor"
	SourceBaseRefMinor       = "base-ref-minor"
	SourceConfigDefaultMinor = "config-default-minor"
)

const (
	EnvRancherVersion     = "RANCHER_VERSION"
	EnvRancherImageTag    = "RANCHER_IMAGE_TAG"
	EnvCertManagerVersion = "CERT_MANAGER_VERSION"
)

// EnvVarForDistro returns the version variable for a distro.
func EnvVarForDistro(distro string) string {
	return strings.ToUpper(distro) + "_VERSION"
}

// chartRepoChannel maps matching versions to a non-default Helm channel.
type chartRepoChannel struct {
	Pattern *regexp.Regexp
	Name    string
	URL     string
}

// chartRepoChannels lists prerelease channels absent from rancher-latest.
var chartRepoChannels = []chartRepoChannel{
	{
		Pattern: regexp.MustCompile(`(?i)-alpha`),
		Name:    "rancher-alpha",
		URL:     "https://releases.rancher.com/server-charts/alpha",
	},
}

// chartRepoForVersion returns the Helm repo name/URL that publishes the given
// Rancher version, or ("", "") when the default "rancher-latest" channel
// (qa-infra-automation's built-in default) already carries it.
func chartRepoForVersion(version string) (name, url string) {
	for _, ch := range chartRepoChannels {
		if ch.Pattern.MatchString(version) {
			return ch.Name, ch.URL
		}
	}
	return "", ""
}

// GitHubClient defines the GitHub operations needed by the resolver.
type GitHubClient interface {
	ListTags(ctx context.Context, owner, repo string) ([]string, error)
	CompareContainsCommit(ctx context.Context, owner, repo, base, head string) (bool, error)
	GetBranchHeadSHA(ctx context.Context, owner, repo, branch string) (string, error)
	BranchExists(ctx context.Context, owner, repo, branch string) (bool, error)
	GetReleaseByTag(ctx context.Context, owner, repo, tag string) (body string, assets []ghclient.ReleaseAsset, ok bool, err error)
}

// HTTPGetter fetches raw content from a URL. Used for KDM data.json,
// Chart.yaml, rancher-images.txt assets, and the cert-manager releases API.
type HTTPGetter interface {
	Get(ctx context.Context, url string) ([]byte, int, error)
}

// Config carries every input needed to resolve versions for one PR.
type Config struct {
	Owner, Repo string
	// BaseRef is the PR's base branch, e.g. "release-v2.14" or "master".
	BaseRef string
	// MergeCommitSHA is the PR's merge commit SHA; empty means the PR has no
	// resolvable commit yet (skips straight to the in-development fallback).
	MergeCommitSHA string
	// IncludePrereleases includes RC/alpha/beta tags as tag-compare
	// candidates and KDM release candidates.
	IncludePrereleases bool
	// UpstreamDistro is the management-cluster distro ("rke2" or "k3s").
	UpstreamDistro string
	// DownstreamDistros are the additional distros (e.g. from
	// CattleConfigTemplate.KubernetesVersionEnvByDistro) to resolve Kubernetes
	// versions for, besides UpstreamDistro.
	DownstreamDistros []string
	// Overrides maps an env-var name (e.g. "RANCHER_VERSION") to a value that
	// always wins over any resolved value. Callers populate this from actual
	// environment variables / config-file defaults before calling Resolve.
	Overrides map[string]string

	// KDMBaseRawURL is the raw-content base for rancher/kontainer-driver-metadata.
	KDMBaseRawURL string
	// KDMReleasesURL is the base URL for the packaged KDM data.json served by
	// releases.rancher.com (the source Rancher's own image build uses), tried
	// as a fallback when KDMBaseRawURL fails.
	KDMReleasesURL string
	// RancherRawBaseURL is the raw-content base for rancher/rancher (used to
	// read chart/Chart.yaml's kubeVersion constraint and package/Dockerfile's
	// chart/KDM branch args).
	RancherRawBaseURL string
	// CertManagerReleasesURL is the GitHub API releases-list URL for
	// cert-manager/cert-manager, used as the terminal cert-manager fallback.
	CertManagerReleasesURL string
	// DefaultMinor is the Rancher "major.minor" (e.g. "2.15") used as the last
	// resort when neither package/Dockerfile nor the base ref yields one.
	DefaultMinor string
}

func (c Config) kdmBaseRawURL() string {
	if c.KDMBaseRawURL != "" {
		return c.KDMBaseRawURL
	}
	return "https://raw.githubusercontent.com/rancher/kontainer-driver-metadata"
}

func (c Config) rancherRawBaseURL() string {
	if c.RancherRawBaseURL != "" {
		return c.RancherRawBaseURL
	}
	return "https://raw.githubusercontent.com/rancher/rancher"
}

func (c Config) certManagerReleasesURL() string {
	if c.CertManagerReleasesURL != "" {
		return c.CertManagerReleasesURL
	}
	return "https://api.github.com/repos/cert-manager/cert-manager/releases"
}

func (c Config) kdmReleasesURL() string {
	if c.KDMReleasesURL != "" {
		return c.KDMReleasesURL
	}
	return "https://releases.rancher.com/kontainer-driver-metadata"
}

// Resolver resolves concrete versions for a single PR/repo context.
type Resolver struct {
	gh   GitHubClient
	http HTTPGetter
	cfg  Config
}

// NewResolver builds a Resolver.
func NewResolver(gh GitHubClient, httpGetter HTTPGetter, cfg Config) *Resolver {
	return &Resolver{gh: gh, http: httpGetter, cfg: cfg}
}

// Resolve returns provenance-aware values, falling back instead of failing.
func (r *Resolver) Resolve(ctx context.Context) *types.VersionResolution {
	minor, minorSource := r.resolveMinor(ctx)
	logrus.Infof("envversions: resolved Rancher minor %q (%s)", minor, minorSource)

	rancherVersion, rancherImageTag, rancherSource, rancherDetail := r.resolveRancherRef(ctx, minor)

	res := &types.VersionResolution{
		RancherVersion:            r.withOverride(EnvRancherVersion, rancherVersion, rancherSource, rancherDetail),
		RancherImageTag:           r.withOverride(EnvRancherImageTag, rancherImageTag, rancherSource, rancherDetail),
		KubernetesVersionByDistro: map[string]types.ResolvedVersion{},
		RancherMinor:              minor,
	}

	kdmBranches := r.candidateKDMBranches(minor, rancherVersion, rancherSource)
	kdmDoc, kdmBranch, kdmErr := r.fetchKDM(ctx, kdmBranches)
	if kdmErr != nil {
		logrus.Warnf("envversions: KDM fetch failed for branches %v: %v", kdmBranches, kdmErr)
	}

	maxKube, haveMaxKube := r.fetchMaxKubeVersion(ctx, rancherVersion, rancherSource)

	distros := dedupDistros(r.cfg.UpstreamDistro, r.cfg.DownstreamDistros)
	for _, distro := range distros {
		val, source, detail := r.resolveK8sVersion(distro, minor, kdmDoc, kdmBranch, maxKube, haveMaxKube)
		res.KubernetesVersionByDistro[distro] = r.withOverride(EnvVarForDistro(distro), val, source, detail)
	}

	cmVal, cmSource, cmDetail := r.resolveCertManager(ctx, rancherVersion, rancherSource)
	res.CertManagerVersion = r.withOverride(EnvCertManagerVersion, cmVal, cmSource, cmDetail)

	// Route the final Rancher version to its required Helm channel.
	res.RancherChartRepoName, res.RancherChartRepoURL = chartRepoForVersion(res.RancherVersion.Value)
	if res.RancherChartRepoName != "" {
		logrus.Infof("envversions: rancher version %q is only published to the %q Helm repo (%s); overriding the default rancher-latest channel",
			res.RancherVersion.Value, res.RancherChartRepoName, res.RancherChartRepoURL)
	}

	return res
}

// reDockerfileBranchArg extracts the "major.minor" from package/Dockerfile
// build args like `ARG CATTLE_KDM_BRANCH=dev-v2.15` or
// `ARG CHART_DEFAULT_BRANCH=dev-v2.15`.
var reDockerfileBranchArg = regexp.MustCompile(`(?m)^ARG\s+(?:CATTLE_KDM_BRANCH|CHART_DEFAULT_BRANCH)\s*=\s*\S*?v(\d+\.\d+)`)

// resolveMinor prefers Dockerfile metadata, base-ref parsing, then config.
func (r *Resolver) resolveMinor(ctx context.Context) (minor, source string) {
	if m := r.fetchDockerfileMinor(ctx); m != "" {
		return m, SourceDockerfileMinor
	}
	if m := parseBaseRefMinor(r.cfg.BaseRef); m != "" {
		return m, SourceBaseRefMinor
	}
	return r.cfg.DefaultMinor, SourceConfigDefaultMinor
}

// fetchDockerfileMinor reads package/Dockerfile at the ref that best
// represents this PR (base branch for an unmerged/main PR, merge commit
// otherwise) and parses the chart/KDM branch build args for the minor.
func (r *Resolver) fetchDockerfileMinor(ctx context.Context) string {
	if r.http == nil {
		return ""
	}
	ref := r.cfg.BaseRef
	if r.cfg.MergeCommitSHA != "" {
		ref = r.cfg.MergeCommitSHA
	}
	if ref == "" {
		return ""
	}
	url := strings.TrimRight(r.cfg.rancherRawBaseURL(), "/") + "/" + ref + "/package/Dockerfile"
	body, status, err := r.http.Get(ctx, url)
	if err != nil || status != 200 {
		return ""
	}
	if m := reDockerfileBranchArg.FindSubmatch(body); m != nil {
		return string(m[1])
	}
	return ""
}

// withOverride applies the env-var override tier, falling back to the
// computed value, or finally to the ${VAR} placeholder when computed is
// empty.
func (r *Resolver) withOverride(envName, computed, source, detail string) types.ResolvedVersion {
	if v, ok := r.cfg.Overrides[envName]; ok && v != "" {
		return types.ResolvedVersion{Value: v, Source: SourceOverride, Detail: "from " + envName}
	}
	if computed == "" {
		return types.ResolvedVersion{Value: "${" + envName + "}", Source: SourcePlaceholder, Detail: detail}
	}
	return types.ResolvedVersion{Value: computed, Source: source, Detail: detail}
}

// reBaseRefMinor extracts "major.minor" from a base branch name such as
// "release-v2.14", "dev-v2.14", or "v2.14". Branches with no embedded version
// (e.g. "master") don't match.
var reBaseRefMinor = regexp.MustCompile(`v(\d+\.\d+)`)

func parseBaseRefMinor(baseRef string) string {
	m := reBaseRefMinor.FindStringSubmatch(baseRef)
	if m == nil {
		return ""
	}
	return m[1]
}

// resolveRancherRef selects the newest containing release or a dev fallback.
func (r *Resolver) resolveRancherRef(ctx context.Context, minor string) (versionValue, imageTagValue, source, detail string) {
	if r.cfg.MergeCommitSHA == "" {
		return r.devFallback(ctx, minor, "PR has no merge commit yet")
	}

	tags, err := r.gh.ListTags(ctx, r.cfg.Owner, r.cfg.Repo)
	if err != nil {
		logrus.Warnf("envversions: listing tags for %s/%s failed: %v", r.cfg.Owner, r.cfg.Repo, err)
		return r.devFallback(ctx, minor, fmt.Sprintf("listing tags failed: %v", err))
	}

	releases, prereleases := partitionCandidateTags(tags, minor)

	if tag, ok := r.newestContainingTag(ctx, releases); ok {
		return tag, tag, SourceTagCompare,
			fmt.Sprintf("newest full release for minor %q containing merge commit %s", minor, shortSHA(r.cfg.MergeCommitSHA))
	}

	// Prereleases remain the fallback when no full release contains the commit.
	if tag, ok := r.newestContainingTag(ctx, prereleases); ok {
		return tag, tag, SourceTagComparePrerel,
			fmt.Sprintf("newest prerelease for minor %q containing merge commit %s (no full release contains it)", minor, shortSHA(r.cfg.MergeCommitSHA))
	}

	nCandidates := len(releases) + len(prereleases)
	return r.devFallback(ctx, minor, fmt.Sprintf("merge commit %s not found in any of %d candidate tag(s)", shortSHA(r.cfg.MergeCommitSHA), nCandidates))
}

// newestContainingTag returns the newest tag (candidates must be ordered
// newest-first) whose history contains the PR's merge commit.
func (r *Resolver) newestContainingTag(ctx context.Context, candidatesNewestFirst []string) (string, bool) {
	for _, tag := range candidatesNewestFirst {
		contains, err := r.gh.CompareContainsCommit(ctx, r.cfg.Owner, r.cfg.Repo, tag, r.cfg.MergeCommitSHA)
		if err != nil {
			logrus.Warnf("envversions: compare %s...%s failed: %v", tag, shortSHA(r.cfg.MergeCommitSHA), err)
			continue
		}
		if contains {
			return tag, true
		}
	}
	return "", false
}

// devFallback resolves the "PR not yet in a release" case. The version label
// is a human-readable "dev-vX.Y" and the image tag mirrors Rancher's CI
// convention (.github/actions/setup-tag-env): "vX.Y-<full-sha>-head" for main
// or "<X.Y>-<full-sha>-head" for a release/vX.Y base branch. The SHA is the
// base branch's current HEAD.
func (r *Resolver) devFallback(ctx context.Context, minor, reason string) (versionValue, imageTagValue, source, detail string) {
	branch := r.cfg.BaseRef
	if branch == "" {
		return "", "", SourcePlaceholder, "no base ref available to resolve an in-development version (" + reason + ")"
	}

	sha, err := r.gh.GetBranchHeadSHA(ctx, r.cfg.Owner, r.cfg.Repo, branch)
	if err != nil {
		logrus.Warnf("envversions: HEAD lookup for branch %q failed: %v", branch, err)
		return "", "", SourcePlaceholder, fmt.Sprintf("base branch %q HEAD lookup failed: %v (%s)", branch, err, reason)
	}

	label := branch
	if minor != "" {
		label = "dev-v" + minor
	}
	imageTag := headImageTag(branch, minor, sha)
	detail = fmt.Sprintf("%s; base branch %q HEAD %s -> CI head image tag %q", reason, branch, shortSHA(sha), imageTag)
	return label, imageTag, SourceInDevelopment, detail
}

// reReleaseBranchRef matches a release branch ref like "release/v2.15" or
// "release-v2.15", capturing the "X.Y".
var reReleaseBranchRef = regexp.MustCompile(`release[/-]v(\d+\.\d+)`)

// headImageTag builds the rancher/rancher dev image tag matching Rancher's CI
// (.github/actions/setup-tag-env): a release branch produces
// "<X.Y>-<sha>-head"; anything else (e.g. main) produces "v<minor>-<sha>-head"
// using the resolved minor. Falls back to a bare SHA only when no minor is
// known.
func headImageTag(branch, minor, sha string) string {
	if m := reReleaseBranchRef.FindStringSubmatch(branch); m != nil {
		return m[1] + "-" + sha + "-head"
	}
	if minor != "" {
		return "v" + minor + "-" + sha + "-head"
	}
	return sha
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// reCandidateTag matches Rancher release tags like "v2.14.3" or
// "v2.14.0-rc1".
var reCandidateTag = regexp.MustCompile(`^v\d+\.\d+\.\d+`)

// partitionCandidateTags splits tags belonging to the given minor line
// ("2.15" -> "v2.15.*") into full releases and prereleases, each ordered
// newest-first. When minor is "" (no parseable minor), every semver-shaped
// tag is a candidate (best effort).
func partitionCandidateTags(tags []string, minor string) (releases, prereleases []string) {
	prefix := ""
	if minor != "" {
		prefix = "v" + minor + "."
	}

	var rel, pre []string
	for _, t := range tags {
		if !reCandidateTag.MatchString(t) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(t, prefix) {
			continue
		}
		if isPrerelease(t) {
			pre = append(pre, t)
		} else {
			rel = append(rel, t)
		}
	}

	return descendingRaw(rel), descendingRaw(pre)
}

// descendingRaw sorts the parseable version strings newest-first.
func descendingRaw(in []string) []string {
	sorted := sortSemverAsc(in)
	out := make([]string, len(sorted))
	for i, sv := range sorted {
		out[len(sorted)-1-i] = sv.raw
	}
	return out
}

func dedupDistros(upstream string, downstream []string) []string {
	seen := map[string]bool{}
	var out []string
	if upstream != "" && !seen[upstream] {
		seen[upstream] = true
		out = append(out, upstream)
	}
	for _, d := range downstream {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// candidateKDMBranches returns the ordered kontainer-driver-metadata branches
// to try for the resolved minor. A prerelease or in-development Rancher
// version lives on the dev branch (release-vX.Y does not exist until GA), so
// dev is tried first in that case; a full release tries the release branch
// first. The other prefix is always included as a bidirectional fallback so a
// missing branch (e.g. release-v2.15 before 2.15 ships) still resolves.
func (r *Resolver) candidateKDMBranches(minor, rancherVersion, rancherSource string) []string {
	if minor == "" {
		return []string{"master"}
	}
	dev := "dev-v" + minor
	release := "release-v" + minor

	devFirst := rancherSource == SourceInDevelopment ||
		rancherSource == SourceTagComparePrerel ||
		isPrerelease(rancherVersion)
	if devFirst {
		return []string{dev, release}
	}
	return []string{release, dev}
}

// kdmDocument is the minimal subset of kontainer-driver-metadata's
// data.json needed to pick a Kubernetes version per distro.
type kdmDocument struct {
	K3s  kdmChannel `json:"k3s"`
	RKE2 kdmChannel `json:"rke2"`
}

type kdmChannel struct {
	Releases    []kdmRelease     `json:"releases"`
	AppDefaults []kdmAppDefaults `json:"appDefaults"`
}

type kdmRelease struct {
	Version string `json:"version"`
}

// kdmAppDefaults maps a Rancher appVersion constraint range to the default
// Kubernetes minor (e.g. "1.36.x") that ships with it.
type kdmAppDefaults struct {
	AppName  string              `json:"appName"`
	Defaults []kdmAppDefaultsRow `json:"defaults"`
}

type kdmAppDefaultsRow struct {
	AppVersion     string `json:"appVersion"`
	DefaultVersion string `json:"defaultVersion"`
}

// fetchKDM fetches and parses the KDM data.json for a branch. It tries the
// raw git layout (…/<branch>/data/data.json) first, then the packaged
// releases.rancher.com layout (…/<branch>/data.json) that Rancher's own image
// build uses.
// fetchKDM fetches and parses the KDM data.json, trying each candidate branch
// in order and, for each, both the raw git layout (…/<branch>/data/data.json)
// and the packaged releases.rancher.com layout (…/<branch>/data.json) that
// Rancher's own image build uses. It returns the first branch that resolves
// along with the parsed document.
func (r *Resolver) fetchKDM(ctx context.Context, branches []string) (*kdmDocument, string, error) {
	if r.http == nil {
		return nil, "", fmt.Errorf("no HTTP getter configured")
	}

	var lastErr error
	for _, branch := range branches {
		urls := []string{
			strings.TrimRight(r.cfg.kdmBaseRawURL(), "/") + "/" + branch + "/data/data.json",
			strings.TrimRight(r.cfg.kdmReleasesURL(), "/") + "/" + branch + "/data.json",
		}
		for _, url := range urls {
			body, status, err := r.http.Get(ctx, url)
			if err != nil {
				lastErr = err
				continue
			}
			if status != 200 {
				lastErr = fmt.Errorf("fetching %s returned status %d", url, status)
				continue
			}
			var doc kdmDocument
			if err := json.Unmarshal(body, &doc); err != nil {
				lastErr = fmt.Errorf("parsing KDM data.json from %s: %w", url, err)
				continue
			}
			return &doc, branch, nil
		}
	}
	return nil, "", lastErr
}

// reKubeVersionField matches the "kubeVersion:" line in a Chart.yaml, e.g.
// `kubeVersion: "< 1.35.0-0"`.
var reKubeVersionField = regexp.MustCompile(`(?m)^kubeVersion:\s*(.+)$`)

// fetchMaxKubeVersion reads rancher/rancher's chart/Chart.yaml at the
// resolved ref (tag, or base branch when still in development) and extracts
// the exclusive upper-bound Kubernetes version from its kubeVersion
// constraint, if any.
func (r *Resolver) fetchMaxKubeVersion(ctx context.Context, rancherVersion, rancherSource string) (string, bool) {
	if r.http == nil {
		return "", false
	}
	ref := rancherVersion
	if rancherSource == SourceInDevelopment {
		ref = r.cfg.BaseRef
	}
	if ref == "" {
		return "", false
	}

	url := strings.TrimRight(r.cfg.rancherRawBaseURL(), "/") + "/" + ref + "/chart/Chart.yaml"
	body, status, err := r.http.Get(ctx, url)
	if err != nil || status != 200 {
		return "", false
	}

	m := reKubeVersionField.FindSubmatch(body)
	if m == nil {
		return "", false
	}
	constraint := strings.Trim(strings.TrimSpace(string(m[1])), `"'`)
	return parseMaxKubeVersionConstraint(constraint)
}

// resolveK8sVersion picks the Kubernetes version for a distro from the KDM
// document. It prefers the version line KDM designates as the default for the
// resolved Rancher minor (appDefaults, e.g. Rancher 2.15 -> "1.36.x") and
// picks the newest concrete release on that line; if there is no matching
// default it falls back to the newest eligible release overall. Prerelease
// gating and the optional chart kubeVersion upper bound are applied in both
// cases.
func (r *Resolver) resolveK8sVersion(distro, minor string, kdmDoc *kdmDocument, kdmBranch string, maxKube string, haveMaxKube bool) (value, source, detail string) {
	if kdmDoc == nil {
		return "", SourcePlaceholder, fmt.Sprintf("KDM data unavailable from branch %q", kdmBranch)
	}

	var channel kdmChannel
	switch distro {
	case "rke2":
		channel = kdmDoc.RKE2
	case "k3s":
		channel = kdmDoc.K3s
	default:
		return "", SourcePlaceholder, fmt.Sprintf("distro %q is not resolved from KDM", distro)
	}

	var eligible []string
	for _, rel := range channel.Releases {
		if rel.Version == "" {
			continue
		}
		if !r.cfg.IncludePrereleases && isPrerelease(rel.Version) {
			continue
		}
		if haveMaxKube && !underMaxKubeVersion(rel.Version, maxKube) {
			continue
		}
		eligible = append(eligible, rel.Version)
	}
	if len(eligible) == 0 {
		return "", SourcePlaceholder, fmt.Sprintf("no eligible %s release found in KDM branch %q", distro, kdmBranch)
	}

	// Prefer KDM's designated default K8s minor for this Rancher version.
	if defMinor, ok := kdmDefaultK8sMinor(channel.AppDefaults, minor); ok {
		if onLine := filterByK8sMinor(eligible, defMinor); len(onLine) > 0 {
			newest := newestRaw(onLine)
			detail = fmt.Sprintf("newest %s %s.x release (KDM default for Rancher %s) from kontainer-driver-metadata@%s", distro, defMinor, minor, kdmBranch)
			if haveMaxKube {
				detail += fmt.Sprintf(" (bounded by chart kubeVersion < %s)", maxKube)
			}
			return newest, SourceKDM, detail
		}
	}

	newest := newestRaw(eligible)
	detail = fmt.Sprintf("newest eligible %s release from kontainer-driver-metadata@%s (no appDefaults match for Rancher %s)", distro, kdmBranch, minor)
	if haveMaxKube {
		detail += fmt.Sprintf(" (bounded by chart kubeVersion < %s)", maxKube)
	}
	return newest, SourceKDM, detail
}

// newestRaw returns the newest (highest semver) of the given version strings.
func newestRaw(in []string) string {
	sorted := sortSemverAsc(in)
	return sorted[len(sorted)-1].raw
}

// kdmDefaultK8sMinor returns the default Kubernetes minor (e.g. "1.36") that
// KDM associates with the given Rancher minor via appDefaults, matching the
// "appName: rancher" entry whose appVersion range contains "<minor>.0".
func kdmDefaultK8sMinor(appDefaults []kdmAppDefaults, rancherMinor string) (string, bool) {
	if rancherMinor == "" {
		return "", false
	}
	target, ok := parseSemver(rancherMinor + ".0")
	if !ok {
		return "", false
	}
	// KDM appDefaults ranges overlap by design (e.g. the 2.14 row is
	// ">= 2.14.0-0 < 2.15.100-0", which also matches 2.15.0). Choose the most
	// specific match: the one with the highest lower bound that still contains
	// the target.
	best := ""
	var bestLower semver
	haveBest := false
	for _, ad := range appDefaults {
		if !strings.EqualFold(ad.AppName, "rancher") {
			continue
		}
		for _, row := range ad.Defaults {
			if !appVersionRangeContains(row.AppVersion, target) {
				continue
			}
			m := reK8sMinor.FindStringSubmatch(row.DefaultVersion)
			if m == nil {
				continue
			}
			lower, hasLower := appVersionLowerBound(row.AppVersion)
			if !haveBest || (hasLower && bestLower.less(lower)) {
				best = m[1]
				bestLower = lower
				haveBest = true
			}
		}
	}
	if haveBest {
		return best, true
	}
	return "", false
}

// appVersionLowerBound returns the parsed ">=" lower bound of a KDM appVersion
// constraint, if present.
func appVersionLowerBound(constraint string) (semver, bool) {
	if m := reAppVersionLower.FindStringSubmatch(constraint); m != nil {
		if lo, ok := parseSemver(m[1]); ok {
			return lo, true
		}
	}
	return semver{}, false
}

// reAppVersionBound matches a ">=" lower bound and "<" upper bound in a KDM
// appVersion constraint string like ">= 2.15.0-0 < 2.16.100-0".
var (
	reAppVersionLower = regexp.MustCompile(`>=?\s*([0-9][0-9A-Za-z.\-]*)`)
	reAppVersionUpper = regexp.MustCompile(`<\s*([0-9][0-9A-Za-z.\-]*)`)
	reK8sMinor        = regexp.MustCompile(`^(\d+\.\d+)`)
)

// appVersionRangeContains reports whether target satisfies the KDM appVersion
// constraint (a ">= lower < upper" range; either bound may be absent).
func appVersionRangeContains(constraint string, target semver) bool {
	if m := reAppVersionLower.FindStringSubmatch(constraint); m != nil {
		if lo, ok := parseSemver(m[1]); ok && target.less(lo) {
			return false
		}
	}
	if m := reAppVersionUpper.FindStringSubmatch(constraint); m != nil {
		if hi, ok := parseSemver(m[1]); ok && !target.less(hi) {
			return false
		}
	}
	return true
}

// filterByK8sMinor keeps only versions on the given Kubernetes minor line
// (e.g. "1.36" keeps "v1.36.2+rke2r1").
func filterByK8sMinor(versions []string, k8sMinor string) []string {
	var out []string
	for _, v := range versions {
		sv, ok := parseSemver(v)
		if !ok {
			continue
		}
		if sv.majorMinor() == k8sMinor {
			out = append(out, v)
		}
	}
	return out
}

// reCertManagerMention loosely matches a cert-manager version mention in
// free-form release-notes text, e.g. "cert-manager v1.14.5" or
// "cert-manager: 1.14.5".
var reCertManagerMention = regexp.MustCompile(`(?i)cert-manager[^0-9\n]{0,20}v?(\d+\.\d+\.\d+)`)

// reCertManagerImageLine matches a cert-manager image reference line from a
// rancher-images.txt asset, e.g.
// "rancher/mirrored-cert-manager-controller:v1.14.5".
var reCertManagerImageLine = regexp.MustCompile(`(?i)cert-manager-controller:v?(\d+\.\d+\.\d+[0-9A-Za-z.-]*)`)

// resolveCertManager applies the cert-manager tier chain: release notes ->
// rancher-images.txt asset -> cert-manager/cert-manager latest stable.
func (r *Resolver) resolveCertManager(ctx context.Context, rancherVersion, rancherSource string) (value, source, detail string) {
	if rancherSource == SourceTagCompare && r.gh != nil {
		body, assets, ok, err := r.gh.GetReleaseByTag(ctx, r.cfg.Owner, r.cfg.Repo, rancherVersion)
		if err != nil {
			logrus.Warnf("envversions: fetching release notes for %s failed: %v", rancherVersion, err)
		}
		if ok {
			if m := reCertManagerMention.FindStringSubmatch(body); m != nil {
				return m[1], SourceReleaseNotes, fmt.Sprintf("parsed from %s release notes", rancherVersion)
			}
			if v := r.extractCertManagerFromImagesTxt(ctx, assets); v != "" {
				return v, SourceImagesTxt, fmt.Sprintf("parsed from rancher-images.txt asset on the %s release", rancherVersion)
			}
		}
	}

	if v := r.fetchCertManagerUpstreamLatest(ctx); v != "" {
		return v, SourceUpstreamLatest, "latest stable cert-manager/cert-manager release"
	}

	return "", SourcePlaceholder, "cert-manager version could not be resolved from release notes, images list, or upstream releases"
}

func (r *Resolver) extractCertManagerFromImagesTxt(ctx context.Context, assets []ghclient.ReleaseAsset) string {
	if r.http == nil {
		return ""
	}
	for _, a := range assets {
		if !strings.EqualFold(a.Name, "rancher-images.txt") || a.BrowserDownloadURL == "" {
			continue
		}
		body, status, err := r.http.Get(ctx, a.BrowserDownloadURL)
		if err != nil || status != 200 {
			continue
		}
		if m := reCertManagerImageLine.FindSubmatch(body); m != nil {
			return string(m[1])
		}
	}
	return ""
}

type certManagerRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
}

func (r *Resolver) fetchCertManagerUpstreamLatest(ctx context.Context) string {
	if r.http == nil {
		return ""
	}
	body, status, err := r.http.Get(ctx, r.cfg.certManagerReleasesURL())
	if err != nil || status != 200 {
		return ""
	}
	var releases []certManagerRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return ""
	}
	for _, rel := range releases {
		if rel.Prerelease || isPrerelease(rel.TagName) {
			continue
		}
		// Strip the leading "v" so this tier's output matches the bare
		// "X.Y.Z" format the release-notes/images-txt tiers already produce
		// (their regex capture groups exclude it) and that
		// qa-infra-automation's rancher vars.yaml documents for
		// cert_manager_version.
		return stripLeadingV(rel.TagName)
	}
	return ""
}

// stripLeadingV removes a single leading "v"/"V" from a version string, if
// present (e.g. "v1.21.0" -> "1.21.0"; "1.21.0" is returned unchanged).
func stripLeadingV(v string) string {
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		return v[1:]
	}
	return v
}
