package ingest

// Temporary stubs so AdapterFor compiles before each real adapter lands. As each
// adapter file is added (Tasks 3-4, 6, 7, 8), delete that adapter's stub methods
// here; when all four are real, delete this file entirely.

type claudeAdapter struct{}
type copilotAdapter struct{}
type codexAdapter struct{}
type geminiAdapter struct{}

func (claudeAdapter) DiscoverFile(string, int64) (string, error) { return "", nil }
func (claudeAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) {
	return nil, Cursor{}, nil
}
func (copilotAdapter) DiscoverFile(string, int64) (string, error) { return "", nil }
func (copilotAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) {
	return nil, Cursor{}, nil
}
func (codexAdapter) DiscoverFile(string, int64) (string, error) { return "", nil }
func (codexAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) {
	return nil, Cursor{}, nil
}
func (geminiAdapter) DiscoverFile(string, int64) (string, error) { return "", nil }
func (geminiAdapter) ParseNewUserMessages(string, Cursor) ([]UserMessage, Cursor, error) {
	return nil, Cursor{}, nil
}
