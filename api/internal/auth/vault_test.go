package auth

import (
	"bytes"
	"testing"
)

func testKey() []byte { return bytes.Repeat([]byte{7}, 32) }

func TestVault_RoundTrip(t *testing.T) {
	t.Parallel()
	v, err := NewVault(testKey())
	if err != nil {
		t.Fatal(err)
	}
	blob, err := v.Encrypt("tok")
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Decrypt(blob)
	if err != nil {
		t.Fatal(err)
	}
	if got != "tok" {
		t.Fatalf("got %q, want %q", got, "tok")
	}
}

func TestVault_DifferentNonceEachCall(t *testing.T) {
	t.Parallel()
	v, _ := NewVault(testKey())
	a, _ := v.Encrypt("tok")
	b, _ := v.Encrypt("tok")
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same input were identical")
	}
}

func TestVault_TamperedBlobFails(t *testing.T) {
	t.Parallel()
	v, _ := NewVault(testKey())
	blob, _ := v.Encrypt("tok")
	blob[len(blob)-1] ^= 0xff
	if _, err := v.Decrypt(blob); err == nil {
		t.Fatal("expected error for tampered blob")
	}
}

func TestVault_ShortBlobFails(t *testing.T) {
	t.Parallel()
	v, _ := NewVault(testKey())
	if _, err := v.Decrypt([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for short blob")
	}
}

func TestNewVault_RejectsShortKey(t *testing.T) {
	t.Parallel()
	if _, err := NewVault(make([]byte, 16)); err == nil {
		t.Fatal("expected error for 16-byte key")
	}
}
