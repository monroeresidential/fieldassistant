package blob

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/google/uuid"
)

func TestKey(t *testing.T) {
	d, a := uuid.New(), uuid.New()
	want := "attachments/7/" + d.String() + "/" + a.String() + ".jpg"
	if got := Key(7, d, a); got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
}

func TestMemory_PutGetDelete(t *testing.T) {
	roundTrip(t, NewMemory(), "attachments/1/a/b.jpg")
}

func TestMemory_Keys(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	for _, k := range []string{"b", "a"} {
		if err := m.Put(ctx, k, "image/jpeg", 1, bytes.NewReader([]byte{1})); err != nil {
			t.Fatal(err)
		}
	}
	keys := m.Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("Keys = %v, want [a b]", keys)
	}
}

func TestS3_RoundTrip(t *testing.T) {
	endpoint := os.Getenv("OBJECT_STORE_ENDPOINT")
	if endpoint == "" {
		t.Skip("OBJECT_STORE_ENDPOINT not set")
	}
	s, err := NewS3(S3Config{
		Endpoint:  endpoint,
		Region:    os.Getenv("OBJECT_STORE_REGION"),
		Bucket:    os.Getenv("OBJECT_STORE_BUCKET"),
		AccessKey: os.Getenv("OBJECT_STORE_ACCESS_KEY"),
		SecretKey: os.Getenv("OBJECT_STORE_SECRET_KEY"),
		PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	// A second call finds the bucket and is a no-op.
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket again: %v", err)
	}
	roundTrip(t, s, Key(0, uuid.New(), uuid.New()))
}

// roundTrip puts, gets and deletes one object, then checks that a missing
// object reads as ErrNotFound and that deleting it again is not an error.
func roundTrip(t *testing.T, s Store, key string) {
	t.Helper()
	ctx := context.Background()
	body := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'e', 'g'}

	if err := s.Put(ctx, key, "image/jpeg", int64(len(body)), bytes.NewReader(body)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, size, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, body) || size != int64(len(body)) {
		t.Errorf("Get = %x (size %d), want %x (size %d)", got, size, body, len(body))
	}

	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := s.Get(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Errorf("second Delete: err = %v, want nil", err)
	}
}
