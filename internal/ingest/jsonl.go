package ingest

import (
	"bufio"
	"io"
	"os"
)

// readCompleteJSONLines reads a JSONL file from byteOffset and returns each
// COMPLETE (newline-terminated) line, plus the offset just past the last complete
// line. A partial final line — one the CLI is still mid-writing, with no trailing
// '\n' yet — is NOT returned and NOT counted, so the cursor stays before it and
// the next poll re-reads it once finished. This prevents the cursor from skipping
// into the middle of a record and permanently losing that message (#65).
//
// A missing file yields (nil, byteOffset, nil) so a not-yet-created session file
// is a no-op. Lines are returned WITH their trailing '\n' stripped; callers
// json.Unmarshal them (JSON ignores surrounding whitespace either way).
func readCompleteJSONLines(path string, byteOffset int64) ([][]byte, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, byteOffset, nil
		}
		return nil, byteOffset, err
	}
	defer f.Close()

	if _, err := f.Seek(byteOffset, io.SeekStart); err != nil {
		return nil, byteOffset, err
	}

	var lines [][]byte
	consumed := byteOffset
	r := bufio.NewReader(f)
	for {
		// ReadBytes returns the data read so far and err==io.EOF when it hit end of
		// file WITHOUT finding the delimiter — that trailing chunk is a partial line
		// still being written, so we stop without consuming it.
		line, rerr := r.ReadBytes('\n')
		if rerr == nil {
			consumed += int64(len(line)) // includes the '\n'
			// Strip the trailing newline for the caller.
			trimmed := line[:len(line)-1]
			if len(trimmed) > 0 {
				cp := make([]byte, len(trimmed))
				copy(cp, trimmed)
				lines = append(lines, cp)
			}
			continue
		}
		if rerr == io.EOF {
			// Whatever ReadBytes returned here has no terminating '\n' → partial,
			// leave it for the next poll (do not advance past it).
			break
		}
		return lines, consumed, rerr
	}
	return lines, consumed, nil
}
