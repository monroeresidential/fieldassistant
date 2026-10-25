package records

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// bodyHash hashes a record body for change detection, ignoring the url of
// every object inside an "attachments" array. Procore signs those download
// URLs per response, so hashing them would move change_seq on every pull of
// an RFI with attachments although nothing the user sees changed. The body
// is decoded with json.Number (ids keep their precision) and re-encoded,
// which sorts object keys, so the hash is of a canonical form. A body that
// does not decode is hashed by its bytes. The stored body is not changed.
func bodyHash(raw []byte) string {
	sum := func(b []byte) string {
		s := sha256.Sum256(b)
		return hex.EncodeToString(s[:])
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return sum(raw)
	}
	stripAttachmentURLs(v)
	canon, err := json.Marshal(v)
	if err != nil {
		return sum(raw)
	}
	return sum(canon)
}

// stripAttachmentURLs deletes "url" from every object in an "attachments"
// array anywhere in v.
func stripAttachmentURLs(v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if arr, ok := child.([]any); ok && k == "attachments" {
				for _, item := range arr {
					if obj, ok := item.(map[string]any); ok {
						delete(obj, "url")
					}
				}
			}
			stripAttachmentURLs(child)
		}
	case []any:
		for _, child := range t {
			stripAttachmentURLs(child)
		}
	}
}
