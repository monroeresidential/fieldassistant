package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// Audit event kinds.
const (
	AuditExtracted = "extracted"
	AuditEdited    = "edited"
)

// auditTurn is one conversation message in the extracted audit payload.
type auditTurn struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// extractedAudit is the payload of an extracted audit event.
type extractedAudit struct {
	Turns        []auditTurn     `json:"turns"`
	ToolCalls    int             `json:"tool_calls"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
	RawFinal     json.RawMessage `json:"raw_final"`
}

func newExtractedAudit(res llm.Result) extractedAudit {
	turns := make([]auditTurn, 0, len(res.Turns))
	for _, t := range res.Turns {
		turns = append(turns, auditTurn{Role: t.Role, Content: t.Content})
	}
	return extractedAudit{
		Turns:        turns,
		ToolCalls:    res.ToolCalls,
		InputTokens:  res.InputTokens,
		OutputTokens: res.OutputTokens,
		RawFinal:     res.Final,
	}
}

// editedAudit is the payload of an edited audit event.
type editedAudit struct {
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
}

// insertAudit writes one audit event for a draft with q, so callers can put
// it in the same transaction as the draft change it records.
func insertAudit(ctx context.Context, q *db.Queries, companyID int64, draftID uuid.UUID, kind string, payload any, durationMS int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("pipeline: encode %s audit: %w", kind, err)
	}
	_, err = q.InsertAuditEvent(ctx, db.InsertAuditEventParams{
		CompanyID:  companyID,
		DraftID:    pgtype.UUID{Bytes: draftID, Valid: true},
		Kind:       kind,
		Payload:    body,
		DurationMs: pgtype.Int4{Int32: int32(durationMS), Valid: durationMS > 0},
	})
	if err != nil {
		return fmt.Errorf("pipeline: insert %s audit: %w", kind, err)
	}
	return nil
}
