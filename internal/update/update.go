// Package update implements the in-app "a newer release is available" check (#83).
// It queries the public GitHub releases API (no auth) for the latest stable release
// and reports whether it is newer than the embedded build version. It NEVER downloads
// or self-replaces — the caller only surfaces a banner and opens a URL.
//
// The whole flow is fail-safe: any network, HTTP, or parse failure returns an error
// with a nil Info, and the startup caller logs-and-ignores it, so a failed check can
// never block startup, show a spurious banner, or surface an error to the user.
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"desktop/internal/version"
)

// DefaultAPIURL is the public GitHub endpoint for the repo's latest (non-prerelease,
// non-draft) release. GitHub itself excludes prereleases and drafts from this route.
const DefaultAPIURL = "https://api.github.com/repos/mytsx/agent-chat/releases/latest"

// releasesTagURLPrefix builds a fallback release-page URL from a tag when the API
// response omits html_url (a degenerate case), so "Notları gör" never dead-links.
const releasesTagURLPrefix = "https://github.com/mytsx/agent-chat/releases/tag/"

// defaultUserAgent is required: GitHub rejects API requests that omit a User-Agent.
const defaultUserAgent = "agent-chat-update-check"

// Asset mirrors the fields of a GitHub release asset we consume.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Release mirrors the subset of the GitHub release object we consume.
type Release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

// Info is the result surfaced to the UI when a newer stable release exists.
type Info struct {
	Version        string `json:"version"`        // latest release version, no leading "v"
	CurrentVersion string `json:"currentVersion"` // the embedded build version
	ReleaseURL     string `json:"releaseURL"`     // release page (always non-empty)
	DMGURL         string `json:"dmgURL"`         // direct .dmg download, "" if none
}

// Checker performs the update check. Client and APIURL are injectable so tests can
// point at an httptest server; both have sensible zero-value defaults.
type Checker struct {
	// Client is the HTTP client used for the request. When nil, a client with a 5s
	// timeout is used so a hung endpoint can never stall the caller.
	Client *http.Client
	// APIURL overrides the GitHub endpoint (tests). Empty means DefaultAPIURL.
	APIURL string
	// UserAgent overrides the request User-Agent. Empty means defaultUserAgent.
	UserAgent string
}

func (c *Checker) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 5 * time.Second}
}

func (c *Checker) apiURL() string {
	if strings.TrimSpace(c.APIURL) != "" {
		return c.APIURL
	}
	return DefaultAPIURL
}

func (c *Checker) userAgent() string {
	if strings.TrimSpace(c.UserAgent) != "" {
		return c.UserAgent
	}
	return defaultUserAgent
}

// Check returns update Info when the latest stable GitHub release is strictly newer
// than current. It returns (nil, nil) when up to date, when current is a dev build
// (no network request is made), or when the latest release is a draft/prerelease. It
// returns (nil, err) only on a network/HTTP/parse failure.
func (c *Checker) Check(ctx context.Context, current string) (*Info, error) {
	// Skip dev/unversioned builds entirely — do not even hit the network.
	if version.IsDevBuild(current) {
		return nil, nil
	}

	rel, err := c.fetchLatest(ctx)
	if err != nil {
		return nil, err
	}

	// GitHub's /latest already excludes these, but guard defensively so a stray
	// draft/prerelease can never surface a banner (recommendation 3: stable only).
	if rel.Draft || rel.Prerelease {
		return nil, nil
	}

	latest := trimVPrefix(rel.TagName)
	if !version.IsStable(latest) {
		return nil, nil // e.g. an "-rc"/"-beta" tag despite prerelease=false
	}
	if !version.Newer(latest, current) {
		return nil, nil
	}

	return &Info{
		Version:        latest,
		CurrentVersion: strings.TrimSpace(current),
		ReleaseURL:     releaseURL(rel),
		DMGURL:         selectDMGAsset(rel),
	}, nil
}

// fetchLatest performs the GitHub API request and decodes the release. A non-2xx
// status is an error (rate-limit 403/429, 404, 5xx …), as is any transport or JSON
// failure.
func (c *Checker) fetchLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("update: request oluşturulamadı: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: istek başarısız: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Drain a small amount so the connection can be reused; ignore the body.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("update: beklenmeyen HTTP durumu %d", resp.StatusCode)
	}

	// Bound the response body so a malicious/huge payload can't exhaust memory.
	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("update: JSON çözümlenemedi: %w", err)
	}
	return &rel, nil
}

// selectDMGAsset returns the download URL of the release's .dmg asset, preferring a
// "universal" build when several .dmg assets are present. Returns "" when none exist.
func selectDMGAsset(r *Release) string {
	var fallback string
	for _, a := range r.Assets {
		if !strings.HasSuffix(strings.ToLower(a.Name), ".dmg") {
			continue
		}
		if strings.Contains(strings.ToLower(a.Name), "universal") {
			return a.BrowserDownloadURL
		}
		if fallback == "" {
			fallback = a.BrowserDownloadURL
		}
	}
	return fallback
}

// releaseURL returns the release page URL, falling back to a tag-derived URL when the
// API response omits html_url.
func releaseURL(r *Release) string {
	if u := strings.TrimSpace(r.HTMLURL); u != "" {
		return u
	}
	if tag := strings.TrimSpace(r.TagName); tag != "" {
		return releasesTagURLPrefix + tag
	}
	return ""
}

// trimVPrefix strips a single leading 'v'/'V' and surrounding whitespace.
func trimVPrefix(tag string) string {
	tag = strings.TrimSpace(tag)
	if len(tag) > 0 && (tag[0] == 'v' || tag[0] == 'V') {
		return tag[1:]
	}
	return tag
}
