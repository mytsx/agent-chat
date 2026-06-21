package hub

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"desktop/internal/types"
	"desktop/internal/validation"
)

// archiveBufferSize bounds the in-flight archive backlog. Truncation fires only
// every ~200 messages and Clear is rare, so this is generous headroom.
const archiveBufferSize = 256

// archiveJob carries a batch of messages leaving a room to the archive writer.
type archiveJob struct {
	room string
	msgs []types.Message
}

// archiveDir returns the directory holding per-room append-only archives.
func (h *Hub) archiveDir() string {
	return filepath.Join(h.dataDir, "hub-state", "archive")
}

// enqueueArchive hands messages leaving a room to the async writer, keeping
// disk I/O off the caller's path. A no-op without a data dir or messages. If
// the hub is shutting down it falls back to a synchronous write so the backlog
// is never lost.
func (h *Hub) enqueueArchive(room string, msgs []types.Message) {
	if len(msgs) == 0 || h.dataDir == "" {
		return
	}
	select {
	case h.archiveCh <- archiveJob{room: room, msgs: msgs}:
	case <-h.done:
		// Writer is stopping/stopped: persist directly rather than drop.
		h.appendArchive(room, msgs)
	}
}

// runArchiveWriter owns all archive file I/O. It drains archiveCh until the hub
// shuts down, then flushes any remaining backlog and signals completion by
// closing archiveDone. Run as a single goroutine started from Run.
func (h *Hub) runArchiveWriter() {
	defer close(h.archiveDone)
	for {
		select {
		case job := <-h.archiveCh:
			h.appendArchive(job.room, job.msgs)
		case <-h.done:
			for {
				select {
				case job := <-h.archiveCh:
					h.appendArchive(job.room, job.msgs)
				default:
					return
				}
			}
		}
	}
}

// appendArchive appends messages to a room's append-only archive at
// hub-state/archive/{room}.jsonl (one JSON-encoded Message per line). It is
// best-effort: every failure is logged, never returned, so it can run on the
// message hot path without breaking room operations. The room name is
// validated to prevent path traversal. An empty data dir or message slice is a
// silent no-op.
func (h *Hub) appendArchive(room string, msgs []types.Message) {
	if len(msgs) == 0 || h.dataDir == "" {
		return
	}
	if err := validation.ValidateName(room); err != nil {
		h.logger.Printf("Archive skipped for invalid room name %q: %v", room, err)
		return
	}

	dir := h.archiveDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		h.logger.Printf("Failed to create archive dir: %v", err)
		return
	}

	var buf bytes.Buffer
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			h.logger.Printf("Failed to marshal archived message (room %s, id %d): %v", room, m.ID, err)
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if buf.Len() == 0 {
		return
	}

	path := filepath.Join(dir, room+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		h.logger.Printf("Failed to open archive for room %s: %v", room, err)
		return
	}
	defer f.Close()

	if _, err := f.Write(buf.Bytes()); err != nil {
		h.logger.Printf("Failed to append archive for room %s: %v", room, err)
	}
}
