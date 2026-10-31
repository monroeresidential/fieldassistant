package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
)

// Memory is an in-memory Store for tests.
type Memory struct {
	mu      sync.Mutex
	objects map[string]memoryObject
}

type memoryObject struct {
	data        []byte
	contentType string
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{objects: map[string]memoryObject{}}
}

// Put stores the bytes read from r. Like S3, it fails when r does not hold
// exactly size bytes.
func (m *Memory) Put(_ context.Context, key, contentType string, size int64, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("blob: read %s: %w", key, err)
	}
	if int64(len(data)) != size {
		return fmt.Errorf("blob: put %s: read %d bytes, want %d", key, len(data), size)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memoryObject{data: data, contentType: contentType}
	return nil
}

// Get returns a reader over a copy of the stored bytes.
func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	obj, ok := m.objects[key]
	if !ok {
		return nil, 0, ErrNotFound
	}
	data := bytes.Clone(obj.data)
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

// Delete removes the object; deleting an absent key is not an error.
func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

// Keys returns the stored keys in sorted order.
func (m *Memory) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
