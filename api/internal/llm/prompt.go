package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
)

// Prompt is a versioned prompt file. Version is the first 12 hex digits of
// the SHA-256 of the file bytes, so any edit yields a new version.
type Prompt struct {
	Text    string
	Version string
}

// LoadPrompt reads the prompt file name from fsys (typically an embed.FS
// holding a feature's prompt.md).
func LoadPrompt(fsys fs.FS, name string) (Prompt, error) {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return Prompt{}, fmt.Errorf("llm: load prompt %s: %w", name, err)
	}
	sum := sha256.Sum256(b)
	return Prompt{Text: string(b), Version: hex.EncodeToString(sum[:])[:12]}, nil
}
