package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// releaseJSON builds a minimal GitHub /releases/latest response body.
const dmgAsset = `{"name":"AgentChat-0.7.0-universal.dmg","browser_download_url":"https://github.com/mytsx/agent-chat/releases/download/v0.7.0/AgentChat-0.7.0-universal.dmg"}`

func newerReleaseJSON() string {
	return `{"tag_name":"v0.7.0","html_url":"https://github.com/mytsx/agent-chat/releases/tag/v0.7.0","draft":false,"prerelease":false,"assets":[` + dmgAsset + `]}`
}

// testChecker returns a Checker whose APIURL points at the given test server.
func testChecker(url string) *Checker {
	return &Checker{Client: http.DefaultClient, APIURL: url}
}

func serve(t *testing.T, status int, body string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCheck_NewerRelease(t *testing.T) {
	srv, _ := serve(t, 200, newerReleaseJSON())
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected update info, got nil")
	}
	if info.Version != "0.7.0" {
		t.Errorf("Version = %q, want 0.7.0", info.Version)
	}
	if info.CurrentVersion != "0.6.0" {
		t.Errorf("CurrentVersion = %q, want 0.6.0", info.CurrentVersion)
	}
	wantDMG := "https://github.com/mytsx/agent-chat/releases/download/v0.7.0/AgentChat-0.7.0-universal.dmg"
	if info.DMGURL != wantDMG {
		t.Errorf("DMGURL = %q, want %q", info.DMGURL, wantDMG)
	}
	wantHTML := "https://github.com/mytsx/agent-chat/releases/tag/v0.7.0"
	if info.ReleaseURL != wantHTML {
		t.Errorf("ReleaseURL = %q, want %q", info.ReleaseURL, wantHTML)
	}
}

func TestCheck_UpToDate(t *testing.T) {
	srv, _ := serve(t, 200, newerReleaseJSON())
	info, err := testChecker(srv.URL).Check(context.Background(), "0.7.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil (up to date), got %+v", info)
	}
}

func TestCheck_CurrentNewerThanRelease(t *testing.T) {
	srv, _ := serve(t, 200, newerReleaseJSON())
	info, err := testChecker(srv.URL).Check(context.Background(), "0.8.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil (local is newer), got %+v", info)
	}
}

func TestCheck_PrereleaseSkipped(t *testing.T) {
	body := `{"tag_name":"v0.7.0","html_url":"h","draft":false,"prerelease":true,"assets":[]}`
	srv, _ := serve(t, 200, body)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil (prerelease), got %+v", info)
	}
}

func TestCheck_DraftSkipped(t *testing.T) {
	body := `{"tag_name":"v0.7.0","html_url":"h","draft":true,"prerelease":false,"assets":[]}`
	srv, _ := serve(t, 200, body)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil (draft), got %+v", info)
	}
}

func TestCheck_PrereleaseTagSkipped(t *testing.T) {
	// Defensive: prerelease flag is false but the tag itself carries an -rc label.
	body := `{"tag_name":"v0.7.0-rc1","html_url":"h","draft":false,"prerelease":false,"assets":[]}`
	srv, _ := serve(t, 200, body)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil (rc tag), got %+v", info)
	}
}

func TestCheck_DevBuildSkipsNetwork(t *testing.T) {
	srv, hits := serve(t, 200, newerReleaseJSON())
	for _, cur := range []string{"dev", "", "0.1.0", "unknown", "0.6.0-dirty"} {
		info, err := testChecker(srv.URL).Check(context.Background(), cur)
		if err != nil {
			t.Errorf("Check(%q) unexpected error: %v", cur, err)
		}
		if info != nil {
			t.Errorf("Check(%q) = %+v, want nil (dev build)", cur, info)
		}
	}
	if h := atomic.LoadInt32(hits); h != 0 {
		t.Errorf("dev build hit the network %d times, want 0", h)
	}
}

func TestCheck_NoDMGAsset(t *testing.T) {
	body := `{"tag_name":"v0.7.0","html_url":"https://example.com/rel","draft":false,"prerelease":false,"assets":[{"name":"AgentChat-0.7.0.zip","browser_download_url":"https://example.com/z.zip"}]}`
	srv, _ := serve(t, 200, body)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected info even without a DMG asset")
	}
	if info.DMGURL != "" {
		t.Errorf("DMGURL = %q, want empty (no dmg asset)", info.DMGURL)
	}
	if info.ReleaseURL != "https://example.com/rel" {
		t.Errorf("ReleaseURL = %q, want the release html_url", info.ReleaseURL)
	}
}

func TestCheck_EmptyHTMLURLFallback(t *testing.T) {
	// A degenerate release with no html_url still yields a usable release page URL
	// constructed from the tag, so "Notları gör" never dead-links.
	body := `{"tag_name":"v0.7.0","html_url":"","draft":false,"prerelease":false,"assets":[]}`
	srv, _ := serve(t, 200, body)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected info")
	}
	want := "https://github.com/mytsx/agent-chat/releases/tag/v0.7.0"
	if info.ReleaseURL != want {
		t.Errorf("ReleaseURL = %q, want fallback %q", info.ReleaseURL, want)
	}
}

func TestCheck_TagWithoutVPrefix(t *testing.T) {
	body := `{"tag_name":"0.7.0","html_url":"https://example.com/rel","draft":false,"prerelease":false,"assets":[]}`
	srv, _ := serve(t, 200, body)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil || info.Version != "0.7.0" {
		t.Fatalf("expected version 0.7.0, got %+v", info)
	}
}

func TestCheck_HTTPErrorStatuses(t *testing.T) {
	for _, status := range []int{403, 404, 429, 500, 503} {
		srv, _ := serve(t, status, `{"message":"nope"}`)
		info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
		if err == nil {
			t.Errorf("status %d: expected error, got nil", status)
		}
		if info != nil {
			t.Errorf("status %d: expected nil info, got %+v", status, info)
		}
	}
}

func TestCheck_MalformedJSON(t *testing.T) {
	srv, _ := serve(t, 200, `{not json`)
	info, err := testChecker(srv.URL).Check(context.Background(), "0.6.0")
	if err == nil {
		t.Error("expected error on malformed JSON")
	}
	if info != nil {
		t.Errorf("expected nil info, got %+v", info)
	}
}

func TestCheck_NetworkError(t *testing.T) {
	// Point at a closed server address so the request fails at the transport layer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // now the address refuses connections
	info, err := testChecker(url).Check(context.Background(), "0.6.0")
	if err == nil {
		t.Error("expected a transport error")
	}
	if info != nil {
		t.Errorf("expected nil info, got %+v", info)
	}
}

func TestCheck_SendsRequiredHeaders(t *testing.T) {
	var gotUA, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		_, _ = w.Write([]byte(newerReleaseJSON()))
	}))
	t.Cleanup(srv.Close)
	if _, err := testChecker(srv.URL).Check(context.Background(), "0.6.0"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUA == "" {
		t.Error("expected a non-empty User-Agent (GitHub rejects requests without one)")
	}
	if gotAccept == "" {
		t.Error("expected an Accept header")
	}
}

func TestHTTPClientDefaultTimeout(t *testing.T) {
	// When no Client is injected (the production path), the check must use a short
	// bounded timeout so a hung endpoint can never stall the startup goroutine (C1).
	got := (&Checker{}).httpClient()
	if got.Timeout != 5*time.Second {
		t.Errorf("default httpClient timeout = %v, want 5s", got.Timeout)
	}
	// An injected client is used as-is.
	custom := &http.Client{Timeout: time.Second}
	if (&Checker{Client: custom}).httpClient() != custom {
		t.Error("injected Client should be returned unchanged")
	}
}

func TestSelectDMGAsset(t *testing.T) {
	tests := []struct {
		name   string
		assets []Asset
		want   string
	}{
		{"none", nil, ""},
		{"single dmg", []Asset{{Name: "a.dmg", BrowserDownloadURL: "u1"}}, "u1"},
		{"prefers universal", []Asset{
			{Name: "AgentChat-arm64.dmg", BrowserDownloadURL: "arm"},
			{Name: "AgentChat-0.7.0-universal.dmg", BrowserDownloadURL: "uni"},
		}, "uni"},
		{"case insensitive suffix", []Asset{{Name: "X.DMG", BrowserDownloadURL: "up"}}, "up"},
		{"ignores non-dmg", []Asset{{Name: "notes.txt", BrowserDownloadURL: "t"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := selectDMGAsset(&Release{Assets: tt.assets}); got != tt.want {
				t.Errorf("selectDMGAsset = %q, want %q", got, tt.want)
			}
		})
	}
}
