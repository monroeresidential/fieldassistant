// Package blob stores draft attachment bytes in an object store: an
// S3-compatible bucket (DigitalOcean Spaces in dev and prod, MinIO locally)
// or, in tests, memory.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

// ErrNotFound is returned by Get when no object exists under the key.
var ErrNotFound = errors.New("blob: not found")

// Store puts, reads and deletes objects by key.
type Store interface {
	// Put writes size bytes from r under key, replacing any existing object.
	Put(ctx context.Context, key, contentType string, size int64, r io.Reader) error
	// Get opens the object under key and returns its size. It returns
	// ErrNotFound when the object is absent. The caller closes rc.
	Get(ctx context.Context, key string) (rc io.ReadCloser, size int64, err error)
	// Delete removes the object under key. It returns nil when the object
	// is already absent.
	Delete(ctx context.Context, key string) error
}

// Key is the object key of one draft attachment:
// attachments/<company_id>/<draft_id>/<attachment_id>.jpg.
func Key(companyID int64, draftID, attachmentID uuid.UUID) string {
	return fmt.Sprintf("attachments/%d/%s/%s.jpg", companyID, draftID, attachmentID)
}
