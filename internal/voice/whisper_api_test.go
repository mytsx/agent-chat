package voice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWhisperTranscribeSendsMultipartAndParses(t *testing.T) {
	var gotModel, gotLang, gotAuth, gotFile string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		}
		gotModel = r.FormValue("model")
		gotLang = r.FormValue("language")
		if f, _, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(f)
			gotFile = string(b)
		} else {
			t.Errorf("FormFile: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"merhaba dünya"}`)
	}))
	defer srv.Close()

	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	text, err := NewWhisperClient("sk-xyz").Transcribe(context.Background(), []byte("RIFFfake"))
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if text != "merhaba dünya" {
		t.Errorf("text = %q, want merhaba dünya", text)
	}
	if gotModel != "whisper-1" {
		t.Errorf("model = %q, want whisper-1", gotModel)
	}
	if gotLang != "tr" {
		t.Errorf("language = %q, want tr", gotLang)
	}
	if gotAuth != "Bearer sk-xyz" {
		t.Errorf("auth = %q, want Bearer sk-xyz", gotAuth)
	}
	if gotFile != "RIFFfake" {
		t.Errorf("file = %q, want RIFFfake", gotFile)
	}
}

func TestWhisperTranscribeTrimsAPIKey(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"text":"merhaba"}`)
	}))
	defer srv.Close()
	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	if _, err := NewWhisperClient("  sk-xyz\n").Transcribe(context.Background(), []byte("RIFFfake")); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if gotAuth != "Bearer sk-xyz" {
		t.Errorf("auth = %q, want Bearer sk-xyz", gotAuth)
	}
}

func TestWhisperTranscribeRejectsInvalidAPIKeyBeforeRequest(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatal("server should not be called for invalid API key")
	}))
	defer srv.Close()
	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	_, err := NewWhisperClient("sk-good\nsecond-line").Transcribe(context.Background(), []byte("RIFFfake"))
	if err == nil {
		t.Fatal("expected invalid API key error")
	}
	if called {
		t.Fatal("invalid API key should fail before making a request")
	}
	if msg := err.Error(); !strings.Contains(msg, "geçersiz") || !strings.Contains(msg, "whitespace/control") {
		t.Fatalf("expected validation context, got %v", err)
	}
}

func TestWhisperTranscribeErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	}))
	defer srv.Close()
	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	if _, err := NewWhisperClient("bad").Transcribe(context.Background(), []byte("x")); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestWhisperTranscribeErrorStatusTruncatesBody(t *testing.T) {
	tooLarge := strings.Repeat("x", maxWhisperErrorBodyBytes+128)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, tooLarge)
	}))
	defer srv.Close()
	old := whisperEndpoint
	whisperEndpoint = srv.URL
	defer func() { whisperEndpoint = old }()

	_, err := NewWhisperClient("bad").Transcribe(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("expected error on 502")
	}
	msg := err.Error()
	if !strings.Contains(msg, "…") {
		t.Fatalf("expected truncated error body to include ellipsis, got %d-byte error", len(msg))
	}
	if strings.Contains(msg, strings.Repeat("x", maxWhisperErrorBodyBytes+1)) {
		t.Fatalf("error body was not truncated: %d-byte error", len(msg))
	}
}
