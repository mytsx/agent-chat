package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// Hand off to the writer without blocking the caller. If the backlog is
	// saturated, write synchronously rather than stall the message path — never
	// drop. appendArchive serializes with the writer via archiveMu.
	//
	// No shutdown bookkeeping is needed here: every enqueueArchive originates in
	// a request handler (truncate/clear via archiveFn), and Shutdown waits on
	// inflightRequests before draining, so no job can be orphaned after drain.
	select {
	case h.archiveCh <- archiveJob{room: room, msgs: msgs}:
	default:
		h.archiveBestEffort(room, msgs)
	}
}

// drainArchiveBacklog synchronously writes any archive jobs still buffered after
// the writer goroutine has exited. Called once during shutdown.
func (h *Hub) drainArchiveBacklog() {
	for {
		select {
		case job := <-h.archiveCh:
			h.archiveBestEffort(job.room, job.msgs)
		default:
			return
		}
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
			h.archiveBestEffort(job.room, job.msgs)
		case <-h.done:
			for {
				select {
				case job := <-h.archiveCh:
					h.archiveBestEffort(job.room, job.msgs)
				default:
					return
				}
			}
		}
	}
}

// archiveBestEffort runs appendArchive and logs any failure without returning
// it. Used by the async paths (writer goroutine, shutdown drain, saturated-
// backlog fallback) where there is no caller to surface the error to.
func (h *Hub) archiveBestEffort(room string, msgs []types.Message) {
	if err := h.appendArchive(room, msgs); err != nil {
		h.logger.Printf("Archive write failed (room %s): %v", room, err)
	}
}

// appendArchive appends messages to a room's append-only archive at
// hub-state/archive/{room}.jsonl (one JSON-encoded Message per line). The room
// name is validated to prevent path traversal. An empty data dir or message
// slice is a silent no-op. The returned error lets synchronous callers
// (archive_room / DeleteTeam) observe a failed preservation; async callers wrap
// it with archiveBestEffort.
//
// Phase-A limitation: the archive grows without bound — there is no rotation,
// size cap, or de-duplication yet. Rotation/compaction is deferred to a later
// phase (see docs/PLAN-room-summary-archive.md).
func (h *Hub) appendArchive(room string, msgs []types.Message) error {
	if len(msgs) == 0 || h.dataDir == "" {
		return nil
	}
	if err := validation.ValidateName(room); err != nil {
		return fmt.Errorf("invalid archive room name %q: %w", room, err)
	}

	// Serialize all archive writes: the async writer goroutine and synchronous
	// callers (archive_room RPC, shutdown drain) can target the same file at
	// once, and concurrent appends could otherwise interleave a partial write.
	h.archiveMu.Lock()
	defer h.archiveMu.Unlock()

	// MkdirAll is kept here (not hoisted to startup) so appendArchive stays
	// self-contained — callable synchronously from archive_room and from tests
	// without the writer goroutine. On an existing dir it is a single cheap stat,
	// and this path is not hot (truncation fires ~every 200 messages).
	dir := h.archiveDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}

	// Encode straight into the buffer (no per-message intermediate slice);
	// Encoder.Encode appends the trailing newline for us.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			h.logger.Printf("Skipping unencodable archived message (room %s, id %d): %v", room, m.ID, err)
			continue
		}
	}
	if buf.Len() == 0 {
		return nil
	}

	path := filepath.Join(dir, room+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open archive %s: %w", room, err)
	}
	// defer is a safety net against a panic leaking the fd; the explicit Close
	// below reports a Close-time failure (e.g. ENOSPC surfacing on flush) on
	// this durability path. The double close on the success path is harmless.
	defer f.Close()

	if _, err := f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("append archive %s: %w", room, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close archive %s: %w", room, err)
	}
	return nil
}
