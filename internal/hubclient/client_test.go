package hubclient

import (
	"testing"

	"desktop/internal/types"
)

func TestEnsureSuccess(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		if err := ensureSuccess("operation", &types.Response{Success: true}); err != nil {
			t.Fatalf("ensureSuccess() error = %v, want nil", err)
		}
	})

	t.Run("failure includes operation and hub error", func(t *testing.T) {
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
