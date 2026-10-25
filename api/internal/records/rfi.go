package records

import (
	"context"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// KindRFI is a project's RFIs.
const KindRFI Kind = "rfi"

// RFIFetcher lists a project's RFIs through the Procore client.
type RFIFetcher struct{ PC *procore.Client }

var _ Fetcher = RFIFetcher{}

// ListSince lists the project's RFIs updated since since, or all of them
// when since is nil.
func (f RFIFetcher) ListSince(ctx context.Context, token string, companyID, projectID int64, since *time.Time) ([]procore.RawRecord, error) {
	return f.PC.ListRFIsRaw(ctx, token, companyID, projectID, since)
}
