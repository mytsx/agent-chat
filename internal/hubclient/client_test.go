package hubclient

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"desktop/internal/types"
)

func TestEnsureSuccess(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		if err := ensureSuccess("operation", &types.Response{Success: true}); err != nil {
			t.Fatalf("ensureSuccess() error = %v, want nil", err)
		}
	})

	t.Run("failure includes operation and hub error", func(t *testing.T) {
		t.Parallel()
		err := ensureSuccess("operation", &types.Response{Success: false, Error: "denied"})
		if err == nil {
			t.Fatal("ensureSuccess() error = nil, want failure")
		}
		want := "operation failed: denied"
		if err.Error() != want {
			t.Fatalf("ensureSuccess() error = %q, want %q", err.Error(), want)
		}
	})
}

func TestDecodeSuccessData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		resp      *types.Response
		wantValue string
		wantErr   string
	}{
		{
			name:      "success decodes payload",
			resp:      &types.Response{Success: true, Data: []byte(`{"value":"ok"}`)},
			wantValue: "ok",
		},
		{
			name:    "hub failure preserves operation context",
			resp:    &types.Response{Success: false, Error: "denied"},
			wantErr: "list_rooms_detailed failed: denied",
		},
		{
			name:    "invalid json is surfaced",
			resp:    &types.Response{Success: true, Data: []byte(`{`)},
			wantErr: "unexpected end of JSON input",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeSuccessData[struct {
				Value string `json:"value"`
			}]("list_rooms_detailed", tt.resp)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeSuccessData returned error: %v", err)
			}
			if got.Value != tt.wantValue {
				t.Fatalf("decoded value = %q, want %q", got.Value, tt.wantValue)
			}
		})
	}
}

func TestDiscoverHubAddrRejectsInvalidPortSources(t *testing.T) {
	t.Run("environment override", func(t *testing.T) {
		t.Setenv("AGENT_CHAT_HUB_PORT", "not-a-port")

		_, err := DiscoverHubAddr(t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "AGENT_CHAT_HUB_PORT") || !strings.Contains(err.Error(), "not-a-port") {
			t.Fatalf("DiscoverHubAddr() error = %v, want invalid AGENT_CHAT_HUB_PORT diagnostic", err)
		}
	})

	t.Run("hub.port file", func(t *testing.T) {
		t.Setenv("AGENT_CHAT_HUB_PORT", "")
		dataDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dataDir, "hub.port"), []byte("\n"), 0644); err != nil {
			t.Fatal(err)
		}

		_, err := DiscoverHubAddr(dataDir)
		if err == nil || !strings.Contains(err.Error(), "hub.port") || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("DiscoverHubAddr() error = %v, want empty hub.port diagnostic", err)
		}
	})
}

func TestConnectWithRetryIncludesLastDialError(t *testing.T) {
	t.Parallel()

	client := New("ws://127.0.0.1:1/ws", log.New(os.Stderr, "", 0))
	err := client.ConnectWithRetry(1)
	if err == nil {
		t.Fatal("ConnectWithRetry() error = nil, want failure")
	}

	msg := err.Error()
	if !strings.Contains(msg, "failed to connect to hub after 1 attempts") || !strings.Contains(msg, "last error:") {
		t.Fatalf("ConnectWithRetry() error = %q, want attempts plus last error", msg)
	}
}
