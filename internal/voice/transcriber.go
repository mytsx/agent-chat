package voice

import "context"

// Transcriber turns recorded audio (a WAV byte buffer) into text. Implemented by
// WhisperClient; mocked in app tests so voice orchestration runs without a network
// call.
type Transcriber interface {
	Transcribe(ctx context.Context, wav []byte) (string, error)
}
