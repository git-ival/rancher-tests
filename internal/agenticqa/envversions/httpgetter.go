package envversions

import (
	"context"
	"io"
	"net/http"
)

// DefaultHTTPGetter is the production HTTPGetter implementation, used for
// raw.githubusercontent.com (KDM data.json, Chart.yaml), GitHub release
// assets, and the cert-manager releases API.
type DefaultHTTPGetter struct {
	// Token, when set, is sent as a Bearer Authorization header. Useful for
	// raising rate limits on api.github.com requests; harmless for
	// raw.githubusercontent.com and arbitrary asset URLs.
	Token string
	// Client is the underlying HTTP client; defaults to http.DefaultClient
	// when nil.
	Client *http.Client
}

// Get fetches url and returns its body and HTTP status code.
func (g DefaultHTTPGetter) Get(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}

	client := g.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
