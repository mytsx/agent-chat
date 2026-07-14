package voice

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestNewWhisperClientHasBoundedTimeout(t *testing.T) {
	c := NewWhisperClient("key")
	if c.http.Timeout <= 0 {
		t.Fatal("WhisperClient HTTP client must carry a bounded timeout so a hung OpenAI response can't block forever")
	}
}
