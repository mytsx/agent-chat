package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// geminiAdapter ingests Gemini CLI's monolithic JSON chat record:
// ~/.gemini/tmp/{sha256(cwd)}/chats/session-*.json — a single object
// {messages:[{type,timestamp,content:[{text}]}]}. type=="user" is a human
// message; content[].text is concatenated. Because it is one JSON object (not
// JSONL) the whole file is re-parsed each tick; Cursor.Count tracks how many user
// messages were already emitted so a re-parse only yields new ones (#65).
type geminiAdapter struct{}

type geminiFile struct {
	Messages []struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Content   []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"messages"`
}

func (geminiAdapter) ParseNewUserMessages(path string, cur Cursor) ([]ParsedMessage, Cursor, error) {
	// Gate the (whole-file) re-read+parse on mtime: skip ticks where the monolithic
	// JSON hasn't changed since the last parse (spec §3/§7; #65). cur.ModTime==0
	// (first poll, or a forced re-parse after an emit failure reset the cursor)
	// always parses.
	info, serr := os.Stat(path)
	if serr != nil {
		if os.IsNotExist(serr) {
			return nil, cur, nil
		}
		return nil, cur, serr
	}
	mt := info.ModTime().UnixNano()
	if cur.ModTime != 0 && mt == cur.ModTime {
		return nil, cur, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cur, nil
		}
		return nil, cur, err
	}
	var gf geminiFile
	if json.Unmarshal(data, &gf) != nil {
		// Partial write mid-append — skip this tick, keep the cursor (don't record
		// the new mtime, so the next tick re-parses once the write completes).
		return nil, cur, nil
	}
	// Collect ALL user messages (with their 1-based index as the commit cursor),
	// then return only those past cur.Count. Per-message After carries no ModTime,
	// so an emit failure (which commits a per-message cursor) forces a re-parse on
	// the next tick to retry — only a fully-successful poll records mt via final.
	var all []ParsedMessage
	for _, m := range gf.Messages {
		if m.Type != "user" {
			continue
		}
		var text string
		for _, c := range m.Content {
			text += c.Text
		}
		if text == "" {
			continue
		}
		all = append(all, ParsedMessage{Content: text, Timestamp: m.Timestamp, After: Cursor{Count: len(all) + 1}})
	}
	final := Cursor{Count: len(all), ModTime: mt}
	if cur.Count >= len(all) {
		return nil, final, nil
	}
	return all[cur.Count:], final, nil
}

func (geminiAdapter) DiscoverFile(cwd string, spawnedAtUnixNano int64, claimed func(string) bool) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(cwd))
	dir := filepath.Join(home, ".gemini", "tmp", hex.EncodeToString(sum[:]), "chats")
	return newestJSONLAfter(dir, "session-*.json", spawnedAtUnixNano, claimed)
}
