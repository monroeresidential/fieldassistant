package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

// GetLookup returns one of a project's lookup lists from the cache. The
// company must be visible to the user; the cache itself checks the project
// with the user's token. Cache-Control: no-cache forces a Procore refetch,
// and an If-None-Match equal to the set's ETag answers 304 with no body.
func (s *Server) GetLookup(ctx context.Context, req GetLookupRequestObject) (GetLookupResponseObject, error) {
	userID := claimsFrom(ctx).UserID
	companyID := req.Params.XCompanyId
	if err := s.requireVisible(ctx, userID, companyID); err != nil {
		return nil, err
	}
	kind, ok := pipeline.ParseKind(string(req.Kind))
	if !ok {
		return nil, badRequest("unknown lookup kind " + string(req.Kind))
	}
	force := req.Params.CacheControl != nil && hasNoCache(*req.Params.CacheControl)
	set, err := s.deps.Lookups.Get(ctx, userID, companyID, req.ProjectId, kind, force)
	if err != nil {
		return nil, s.procoreFailure("lookups", err)
	}

	etag := set.ETag
	if etag == "" {
		// A per-user denial is never stored, so it has no ETag of its own.
		sum := sha256.Sum256([]byte("unavailable\x00" + set.Reason))
		etag = `"u` + hex.EncodeToString(sum[:])[:16] + `"`
	}
	if req.Params.IfNoneMatch != nil && *req.Params.IfNoneMatch == etag {
		return GetLookup304Response{}, nil
	}

	rows := make([]LookupRow, 0, len(set.Rows))
	for _, r := range set.Rows {
		row := LookupRow{Id: r.ID, Label: r.Label}
		if len(r.Extra) > 0 {
			row.Extra = &r.Extra
		}
		rows = append(rows, row)
	}
	body := LookupResponse{
		Kind:        LookupResponseKind(set.Kind),
		Rows:        rows,
		Unavailable: set.Unavailable,
		FetchedAt:   set.FetchedAt,
	}
	if set.Unavailable {
		body.Reason = &set.Reason
	}
	return GetLookup200JSONResponse{Body: body, Headers: GetLookup200ResponseHeaders{ETag: etag}}, nil
}

// hasNoCache reports whether a Cache-Control value carries the no-cache
// directive.
func hasNoCache(v string) bool {
	for _, d := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(d), "no-cache") {
			return true
		}
	}
	return false
}
