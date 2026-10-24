package llm

import (
	"testing"
	"testing/fstest"
)

func TestLoadPrompt_VersionIsHashPrefix(t *testing.T) {
	fsys := fstest.MapFS{"prompt.md": {Data: []byte("hello")}}
	p, err := LoadPrompt(fsys, "prompt.md")
	if err != nil {
		t.Fatalf("LoadPrompt: %v", err)
	}
	if p.Text != "hello" {
		t.Errorf("Text = %q, want hello", p.Text)
	}
	if p.Version != "2cf24dba5fb0" {
		t.Errorf("Version = %q, want 2cf24dba5fb0", p.Version)
	}
}

func TestLoadPrompt_MissingFile(t *testing.T) {
	if _, err := LoadPrompt(fstest.MapFS{}, "prompt.md"); err == nil {
		t.Fatal("LoadPrompt on a missing file: want error, got nil")
	}
}
