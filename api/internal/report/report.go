// Package report builds the weekly pilot summary from the audit trail:
// drafts, corrections, duplicates, extraction times, photos and the drafts
// that needed the most fixing. It only reads; cmd/pilotreport prints it.
package report

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// Window is the half-open range [From, To) of draft creation times.
type Window struct{ From, To time.Time }

// Summary is one window's pilot summary.
type Summary struct {
	Window Window
	Drafts struct {
		// Created counts drafts created in the window; Built those with an
		// extraction; Submitted and Failed those in that status now.
		Created, Built, Submitted, Failed int
		FailedByCode                      map[string]int
	}
	Corrections struct {
		// PerDraft has every built draft, with its correction count.
		PerDraft      map[uuid.UUID]int
		DraftsOverTwo int
		// Histogram maps a correction count to the drafts that have it.
		Histogram map[int]int
	}
	Duplicates struct {
		// MultipleSubmitted are drafts with more than one submitted event.
		MultipleSubmitted []uuid.UUID
		// UnknownAttempts are drafts holding a POST whose outcome is still
		// unknown at the window's end.
		UnknownAttempts []uuid.UUID
		// MirrorMatches are RFIs in the mirror that look like a second copy
		// of a submitted draft.
		MirrorMatches []MirrorMatch
	}
	Extraction struct {
		P50, P95 time.Duration
		N        int
	}
	Photos struct {
		Pushed       int
		FailedByCode map[string]int
	}
	// PerUser is sorted by name.
	PerUser []UserRow
	// Worst are up to five drafts with the most corrections, most first.
	Worst []WorstDraft
}

// MirrorMatch is an RFI in the project mirror with a submitted draft's
// subject and creator that is not the draft's own RFI.
type MirrorMatch struct {
	DraftID       uuid.UUID
	DraftRecordID int64
	ProjectID     int64
	ProcoreID     int64
	Subject       string
}

// UserRow is one user's week.
type UserRow struct {
	Name                                    string
	Created, Submitted, Failed, Corrections int
}

// WorstDraft is a heavily corrected draft, with what a reviewer needs to
// turn it into an eval case.
type WorstDraft struct {
	ID         uuid.UUID
	User       string
	CreatedAt  time.Time
	Fields     []string
	Transcript string
	Extracted  json.RawMessage
	Final      json.RawMessage
}

// Draft statuses and audit kinds, as written by internal/pipeline.
const (
	statusSubmitted = "submitted"
	statusFailed    = "failed"

	auditSubmitted = "submitted"
	auditFailed    = "failed"
	auditAttempt   = "attempt"

	attachmentUploaded = "uploaded"
	attachmentFailed   = "failed"
)

// noCode stands in for a failure without an error code.
const noCode = "(no code)"

// unknownUser names a draft whose user row is gone.
const unknownUser = "(unknown user)"

// worstCount is how many drafts the Worst list holds.
const worstCount = 5

// Build reads the window's drafts and their trail and summarises them. It
// issues only selects.
func Build(ctx context.Context, q *db.Queries, w Window) (Summary, error) {
	var s Summary
	s.Window = w
	s.Drafts.FailedByCode = map[string]int{}
	s.Corrections.PerDraft = map[uuid.UUID]int{}
	s.Corrections.Histogram = map[int]int{}
	s.Photos.FailedByCode = map[string]int{}

	drafts, err := q.ListDraftsInWindow(ctx, db.ListDraftsInWindowParams{
		FromAt: pgtype.Timestamptz{Time: w.From, Valid: true},
		ToAt:   pgtype.Timestamptz{Time: w.To, Valid: true},
	})
	if err != nil {
		return s, fmt.Errorf("report: list drafts: %w", err)
	}
	if len(drafts) == 0 {
		return s, nil
	}

	draftIDs := make([]pgtype.UUID, 0, len(drafts))
	userIDs := []pgtype.UUID{}
	for _, d := range drafts {
		draftIDs = append(draftIDs, d.ID)
		if !slices.Contains(userIDs, d.UserID) {
			userIDs = append(userIDs, d.UserID)
		}
	}
	users, err := q.ListUsersByIDs(ctx, userIDs)
	if err != nil {
		return s, fmt.Errorf("report: list users: %w", err)
	}
	type userInfo struct {
		name      string
		procoreID int64
	}
	userByID := map[uuid.UUID]userInfo{}
	for _, u := range users {
		userByID[uuid.UUID(u.ID.Bytes)] = userInfo{name: u.Name, procoreID: u.ProcoreUserID}
	}
	nameOf := func(id pgtype.UUID) string {
		if u, ok := userByID[uuid.UUID(id.Bytes)]; ok {
			return u.name
		}
		return unknownUser
	}

	events, err := q.ListAuditEventsForDrafts(ctx, db.ListAuditEventsForDraftsParams{
		DraftIds: draftIDs,
		Kinds:    []string{auditSubmitted, auditFailed, auditAttempt},
		BeforeAt: pgtype.Timestamptz{Time: w.To, Valid: true},
	})
	if err != nil {
		return s, fmt.Errorf("report: list audit events: %w", err)
	}
	eventsByDraft := map[uuid.UUID][]db.ListAuditEventsForDraftsRow{}
	for _, ev := range events {
		id := uuid.UUID(ev.DraftID.Bytes)
		eventsByDraft[id] = append(eventsByDraft[id], ev)
	}

	// Drafts, corrections, extraction times and per-user rows.
	perUser := map[uuid.UUID]*UserRow{}
	var extractMS []int
	var worst []WorstDraft
	for _, d := range drafts {
		id := uuid.UUID(d.ID.Bytes)
		name := nameOf(d.UserID)
		row := perUser[uuid.UUID(d.UserID.Bytes)]
		if row == nil {
			row = &UserRow{Name: name}
			perUser[uuid.UUID(d.UserID.Bytes)] = row
		}
		s.Drafts.Created++
		row.Created++
		switch d.Status {
		case statusSubmitted:
			s.Drafts.Submitted++
			row.Submitted++
		case statusFailed:
			s.Drafts.Failed++
			row.Failed++
			s.Drafts.FailedByCode[codeOr(d.ErrorCode)]++
		}
		if d.ExtractMs.Valid {
			extractMS = append(extractMS, int(d.ExtractMs.Int32))
		}
		if d.LlmOutput == nil {
			continue
		}
		s.Drafts.Built++
		fields := Corrections(d.LlmOutput, d.UserPayload)
		n := len(fields)
		s.Corrections.PerDraft[id] = n
		s.Corrections.Histogram[n]++
		row.Corrections += n
		if n > 2 {
			s.Corrections.DraftsOverTwo++
		}
		if n > 0 {
			worst = append(worst, WorstDraft{
				ID: id, User: name, CreatedAt: d.CreatedAt.Time, Fields: fields,
				Transcript: d.Transcript, Extracted: d.LlmOutput, Final: d.UserPayload,
			})
		}
	}
	// drafts are oldest first and the sort is stable, so ties keep that order.
	sort.SliceStable(worst, func(i, j int) bool { return len(worst[i].Fields) > len(worst[j].Fields) })
	s.Worst = worst[:min(worstCount, len(worst))]
	for _, row := range perUser {
		s.PerUser = append(s.PerUser, *row)
	}
	slices.SortStableFunc(s.PerUser, func(a, b UserRow) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Created, b.Created))
	})
	s.Extraction.N = len(extractMS)
	s.Extraction.P50 = time.Duration(percentile(extractMS, 0.50)) * time.Millisecond
	s.Extraction.P95 = time.Duration(percentile(extractMS, 0.95)) * time.Millisecond

	// Duplicates from the trail.
	for _, d := range drafts {
		evs := eventsByDraft[uuid.UUID(d.ID.Bytes)]
		submitted := 0
		for _, ev := range evs {
			if ev.Kind == auditSubmitted {
				submitted++
			}
		}
		if submitted > 1 {
			s.Duplicates.MultipleSubmitted = append(s.Duplicates.MultipleSubmitted, uuid.UUID(d.ID.Bytes))
		}
		if unknownAttempt(evs) {
			s.Duplicates.UnknownAttempts = append(s.Duplicates.UnknownAttempts, uuid.UUID(d.ID.Bytes))
		}
	}

	// Duplicates in the mirror: each submitted draft's subjects are its
	// final subject and every subject an attempt posted.
	subjectsByDraft := map[uuid.UUID][]string{}
	var projects []int64
	var allSubjects []string
	for _, d := range drafts {
		if d.Status != statusSubmitted || !d.ProcoreRecordID.Valid {
			continue
		}
		id := uuid.UUID(d.ID.Bytes)
		var subjects []string
		add := func(subj string) {
			if subj != "" && !slices.Contains(subjects, subj) {
				subjects = append(subjects, subj)
			}
		}
		final := d.UserPayload
		if final == nil {
			final = d.LlmOutput
		}
		add(subjectOf(final))
		for _, ev := range eventsByDraft[id] {
			if ev.Kind == auditAttempt {
				add(subjectOf(ev.Payload))
			}
		}
		if len(subjects) == 0 {
			continue
		}
		subjectsByDraft[id] = subjects
		if !slices.Contains(projects, d.ProjectID) {
			projects = append(projects, d.ProjectID)
		}
		for _, subj := range subjects {
			if !slices.Contains(allSubjects, subj) {
				allSubjects = append(allSubjects, subj)
			}
		}
	}
	if len(subjectsByDraft) > 0 {
		rfis, err := q.ListMirrorRFIsForProjectSubjects(ctx, db.ListMirrorRFIsForProjectSubjectsParams{
			ProjectIds: projects, Subjects: allSubjects,
		})
		if err != nil {
			return s, fmt.Errorf("report: list mirror RFIs: %w", err)
		}
		for _, d := range drafts {
			id := uuid.UUID(d.ID.Bytes)
			subjects, ok := subjectsByDraft[id]
			if !ok {
				continue
			}
			creator, known := userByID[uuid.UUID(d.UserID.Bytes)]
			if !known {
				continue
			}
			for _, r := range rfis {
				if r.CompanyID != d.CompanyID || r.ProjectID != d.ProjectID ||
					r.ProcoreID == d.ProcoreRecordID.Int64 || r.CreatedByID != creator.procoreID ||
					!slices.Contains(subjects, r.Subject) {
					continue
				}
				s.Duplicates.MirrorMatches = append(s.Duplicates.MirrorMatches, MirrorMatch{
					DraftID: id, DraftRecordID: d.ProcoreRecordID.Int64,
					ProjectID: r.ProjectID, ProcoreID: r.ProcoreID, Subject: r.Subject,
				})
			}
		}
	}

	// Photos.
	attachments, err := q.ListAttachmentsForDrafts(ctx, draftIDs)
	if err != nil {
		return s, fmt.Errorf("report: list attachments: %w", err)
	}
	for _, a := range attachments {
		switch a.State {
		case attachmentUploaded:
			s.Photos.Pushed++
		case attachmentFailed:
			s.Photos.FailedByCode[codeOr(a.ErrorCode)]++
		}
	}
	return s, nil
}

// failedResponse holds the members of a failed audit event's
// procore_response that resolve an attempt (pipeline's failedAudit).
type failedResponse struct {
	Rejected  bool `json:"rejected"`
	ClosesRun bool `json:"closes_unknown_run"`
}

// unknownAttempt reports whether a draft's trail (submitted, failed and
// attempt events, oldest first) ends with an attempt whose outcome is
// unknown. It follows the rules of pipeline's priorAttempts: walking back
// from the newest event, the current run ends at a submitted event or at a
// failure that closed the run (closes_unknown_run). Inside the run, an
// attempt marker is a POST that may have created an RFI unless the event
// right after it is a rejection of that POST (rejected). Other failures
// (a timeout, a retry) leave the attempt unknown.
func unknownAttempt(evs []db.ListAuditEventsForDraftsRow) bool {
	rejectedAfter := false
	for i := len(evs) - 1; i >= 0; i-- {
		ev := evs[i]
		resolved := rejectedAfter
		rejectedAfter = false
		switch ev.Kind {
		case auditSubmitted:
			return false
		case auditFailed:
			var r failedResponse
			if json.Unmarshal(ev.ProcoreResponse, &r) == nil {
				if r.ClosesRun {
					return false
				}
				rejectedAfter = r.Rejected
			}
		case auditAttempt:
			if !resolved {
				return true
			}
		}
	}
	return false
}

// subjectOf is the subject member of a payload or attempt marker.
func subjectOf(raw json.RawMessage) string {
	var v struct {
		Subject string `json:"subject"`
	}
	if raw == nil || json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.Subject
}

func codeOr(t pgtype.Text) string {
	if !t.Valid || t.String == "" {
		return noCode
	}
	return t.String
}

// percentile is the nearest-rank percentile of ms, as in the eval runner;
// 0 when ms is empty.
func percentile(ms []int, p float64) int {
	if len(ms) == 0 {
		return 0
	}
	s := slices.Clone(ms)
	slices.Sort(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	return s[max(i, 0)]
}
