package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	gh "github.com/google/go-github/v68/github"
)

// newTestClient builds a Client whose underlying go-github client points at
// the given test server instead of api.github.com.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	baseURL, err := url.Parse(srv.URL + "/")
	if err != nil {
		t.Fatalf("parsing test server URL: %v", err)
	}
	c := gh.NewClient(srv.Client())
	c.BaseURL = baseURL
	return &Client{client: c, token: "test-token"}
}

func TestParseRepo(t *testing.T) {
	tests := []struct {
		in        string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"rancher/rancher", "rancher", "rancher", false},
		{"owner/repo/extra", "owner", "repo/extra", false},
		{"norepoformat", "", "", true},
		{"", "", "", true},
		{"/repo", "", "", true},
		{"owner/", "", "", true},
	}
	for _, tc := range tests {
		owner, repo, err := ParseRepo(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseRepo(%q): expected error, got none", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRepo(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if owner != tc.wantOwner || repo != tc.wantRepo {
			t.Errorf("ParseRepo(%q) = (%q, %q), want (%q, %q)", tc.in, owner, repo, tc.wantOwner, tc.wantRepo)
		}
	}
}

func TestGetPRInfo_MergedFields(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"number":           42,
			"title":            "Fix thing",
			"body":             "body",
			"user":             map[string]any{"login": "alice"},
			"head":             map[string]any{"ref": "alice/fix"},
			"base":             map[string]any{"ref": "release-v2.14"},
			"html_url":         "https://github.com/o/r/pull/42",
			"merged":           true,
			"merge_commit_sha": "deadbeef",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	info, err := c.GetPRInfo(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetPRInfo: %v", err)
	}
	if !info.Merged {
		t.Errorf("Merged = false, want true")
	}
	if info.MergeCommitSHA != "deadbeef" {
		t.Errorf("MergeCommitSHA = %q, want %q", info.MergeCommitSHA, "deadbeef")
	}
	if info.Base != "release-v2.14" {
		t.Errorf("Base = %q, want %q", info.Base, "release-v2.14")
	}
}

func TestListTags_Paginates(t *testing.T) {
	// go-github parses the Link header URL as-is; use an absolute URL rooted
	// at the test server so pagination follows correctly.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("/repos/o/r/tags", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "v2.14.1"}})
			return
		}
		w.Header().Set("Link", `<http://`+r.Host+`/repos/o/r/tags?page=2>; rel="next"`)
		_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "v2.14.0"}})
	})
	srv2 := httptest.NewServer(mux2)
	defer srv2.Close()

	c := newTestClient(t, srv2)
	tags, err := c.ListTags(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 2 || tags[0] != "v2.14.0" || tags[1] != "v2.14.1" {
		t.Errorf("ListTags = %v, want [v2.14.0 v2.14.1]", tags)
	}
}

func TestCompareContainsCommit(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"identical", true},
		{"behind", true},
		{"ahead", false},
		{"diverged", false},
	}
	for _, tc := range tests {
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/o/r/compare/base...head", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": tc.status})
		})
		srv := httptest.NewServer(mux)

		c := newTestClient(t, srv)
		got, err := c.CompareContainsCommit(context.Background(), "o", "r", "base", "head")
		srv.Close()
		if err != nil {
			t.Fatalf("CompareContainsCommit(status=%s): %v", tc.status, err)
		}
		if got != tc.want {
			t.Errorf("CompareContainsCommit(status=%s) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestGetBranchHeadSHA(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/git/ref/heads/release-v2.14", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ref":    "refs/heads/release-v2.14",
			"object": map[string]any{"sha": "cafef00d", "type": "commit"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)
	sha, err := c.GetBranchHeadSHA(context.Background(), "o", "r", "release-v2.14")
	if err != nil {
		t.Fatalf("GetBranchHeadSHA: %v", err)
	}
	if sha != "cafef00d" {
		t.Errorf("GetBranchHeadSHA = %q, want %q", sha, "cafef00d")
	}
}

func TestGetReleaseByTag(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/releases/tags/v2.14.0", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v2.14.0",
			"body":     "cert-manager v1.14.5 is required",
			"assets": []map[string]any{
				{"name": "rancher-images.txt", "browser_download_url": "http://example.com/rancher-images.txt"},
			},
		})
	})
	mux.HandleFunc("/repos/o/r/releases/tags/v9.9.9", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)

	body, assets, ok, err := c.GetReleaseByTag(context.Background(), "o", "r", "v2.14.0")
	if err != nil {
		t.Fatalf("GetReleaseByTag: %v", err)
	}
	if !ok {
		t.Fatalf("GetReleaseByTag: ok = false, want true")
	}
	if body != "cert-manager v1.14.5 is required" {
		t.Errorf("body = %q", body)
	}
	if len(assets) != 1 || assets[0].Name != "rancher-images.txt" {
		t.Errorf("assets = %+v", assets)
	}

	_, _, ok, err = c.GetReleaseByTag(context.Background(), "o", "r", "v9.9.9")
	if err != nil {
		t.Fatalf("GetReleaseByTag(missing): unexpected error: %v", err)
	}
	if ok {
		t.Errorf("GetReleaseByTag(missing): ok = true, want false")
	}
}

func TestBranchExists(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r/branches/release-v2.14", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "release-v2.14"})
	})
	mux.HandleFunc("/repos/o/r/branches/dev-v2.14", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Branch not found"}`, http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := newTestClient(t, srv)

	ok, err := c.BranchExists(context.Background(), "o", "r", "release-v2.14")
	if err != nil {
		t.Fatalf("BranchExists(release-v2.14): %v", err)
	}
	if !ok {
		t.Errorf("BranchExists(release-v2.14) = false, want true")
	}

	ok, err = c.BranchExists(context.Background(), "o", "r", "dev-v2.14")
	if err != nil {
		t.Fatalf("BranchExists(dev-v2.14): %v", err)
	}
	if ok {
		t.Errorf("BranchExists(dev-v2.14) = true, want false")
	}
}
