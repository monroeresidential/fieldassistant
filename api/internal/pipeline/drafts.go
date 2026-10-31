package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/llm"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
)

// Draft statuses.
const (
	StatusExtracted  = "extracted"
	StatusReviewed   = "reviewed"
	StatusSubmitting = "submitting"
	StatusSubmitted  = "submitted"
	StatusFailed     = "failed"
)

const (
	// FinalTool is the tool whose input is the extraction.
	FinalTool = "submit_draft"
	// maxToolCalls is the lookup budget per extraction; the prompt says the same.
	maxToolCalls = 6
	// maxToolRows caps the rows one lookup tool call returns.
	maxToolRows = 25
	// maxListDrafts caps List, matching the contract's limit maximum.
	maxListDrafts = 50
)

var (
	// ErrNotFound means the draft does not exist or belongs to another user.
	ErrNotFound = errors.New("pipeline: draft not found")
	// ErrExtractFailed wraps an extractor failure; no draft is stored.
	ErrExtractFailed = errors.New("pipeline: extraction failed")
	// ErrDraftLocked means the draft is being or has been submitted.
	ErrDraftLocked = errors.New("pipeline: draft is submitted or being submitted")
	// ErrDraftChanged means the draft kept changing while Submit validated it.
	ErrDraftChanged = errors.New("pipeline: draft changed during submit")
)

// Draft is one capture moving through extract, review and submit.
type Draft struct {
	ID                   uuid.UUID
	CompanyID, ProjectID int64
	UserID               uuid.UUID
	Feature, Status      string
	Transcript           string
	CapturedAt           time.Time
	// LLMOutput is the validated extraction; UserPayload the reviewer's edit.
	LLMOutput, UserPayload      json.RawMessage
	Unresolved                  []Unresolved
	Confidence                  map[string]float64
	ModelVersion, PromptVersion string
	ExtractMS                   int
	// JobID is the submit job, once one is queued.
	JobID  int64
	Result *Result
	Error  *DraftError
	// Attachments are the draft's photos in manifest order.
	Attachments          []Attachment
	CreatedAt, UpdatedAt time.Time
}

// Result is the Procore record a submitted draft created.
type Result struct {
	ProcoreID   int64
	Number, URL string
}

// DraftError is why a draft failed.
type DraftError struct {
	Code, Message string
}

// Payload is the reviewer's payload when there is one, else the extraction.
func (d Draft) Payload() json.RawMessage {
	if len(d.UserPayload) > 0 {
		return d.UserPayload
	}
	return d.LLMOutput
}

// CreateInput is a new capture to extract. ID is chosen by the client.
type CreateInput struct {
	ID                   uuid.UUID
	UserID               uuid.UUID
	CompanyID, ProjectID int64
	Feature, Transcript  string
	CapturedAt           time.Time
	// Attachments is the photo manifest, at most MaxAttachments entries.
	Attachments []AttachmentInput
}

// Drafts runs extraction and submission and stores drafts and their audit
// trail.
type Drafts struct {
	st        *store.Store
	lookups   *Lookups
	extractor llm.Extractor
	procore   ProcoreRFIs
	tokens    AccessTokenSource
	blobs     blob.Store
	features  map[string]Feature
	// featureStates, when set, is told the outcome of each RFI create (see
	// Features.RecordCreateOutcome). Nil is allowed.
	featureStates *Features
	model         string
	// llmTimeout is the extractor's per-request timeout; zero means none.
	llmTimeout time.Duration
	now        func() time.Time
	log        *slog.Logger
	flight     singleflight.Group

	// beforeMarkSubmitting, when set by a test, runs in Submit between
	// validation and the transition to submitting.
	beforeMarkSubmitting func()
}

// NewDrafts builds a Drafts. pc and tokens post submitted drafts to Procore
// as the draft's user; blobs holds attachment bytes. model is recorded when
// the extractor reports none. llmTimeout is the extractor's per-request
// timeout (zero for none); it bounds a detached extraction (see Create). A
// nil logger means slog.Default().
func NewDrafts(st *store.Store, lookups *Lookups, extractor llm.Extractor, pc ProcoreRFIs, tokens AccessTokenSource, blobs blob.Store, features map[string]Feature, model string, llmTimeout time.Duration, now func() time.Time, logger *slog.Logger) *Drafts {
	if logger == nil {
		logger = slog.Default()
	}
	return &Drafts{st: st, lookups: lookups, extractor: extractor, procore: pc, tokens: tokens, blobs: blobs, features: features, model: model, llmTimeout: llmTimeout, now: now, log: logger}
}

// SetFeatureStates gives d the Features to tell each RFI create's outcome,
// for the create_outcome probe. Call it before d is used.
func (d *Drafts) SetFeatureStates(f *Features) {
	d.featureStates = f
}

// createSlack is the time a detached extraction gets beyond the
// extractor's own bound, for the lookups and the store writes.
const createSlack = 30 * time.Second

// Create returns the draft with in.ID, extracting it first if it does not
// exist. Concurrent calls with one ID share a single extraction. The
// extraction runs detached from any one caller's context, bounded by the
// extractor's loop timeout plus createSlack, so a retry that joined it gets
// the draft even when the request that started it is cancelled; a cancelled
// caller returns its context's error at once and the extraction goes on. A
// draft with that ID owned by another user or company is reported as
// ErrNotFound. The manifest's attachments are stored pending with the
// draft; a manifest over MaxAttachments is ErrTooManyAttachments and
// nothing is extracted or stored.
func (d *Drafts) Create(ctx context.Context, in CreateInput) (Draft, error) {
	if len(in.Attachments) > MaxAttachments {
		return Draft{}, ErrTooManyAttachments
	}
	ch := d.flight.DoChan(in.ID.String(), func() (any, error) {
		fctx := context.WithoutCancel(ctx)
		if bound := llm.LoopTimeout(d.llmTimeout, maxToolCalls); bound > 0 {
			var cancel context.CancelFunc
			fctx, cancel = context.WithTimeout(fctx, bound+createSlack)
			defer cancel()
		}
		return d.create(fctx, in)
	})
	var r singleflight.Result
	select {
	case r = <-ch:
	case <-ctx.Done():
		return Draft{}, ctx.Err()
	}
	if r.Err != nil {
		return Draft{}, r.Err
	}
	dr := r.Val.(Draft)
	if dr.UserID != in.UserID || dr.CompanyID != in.CompanyID {
		return Draft{}, ErrNotFound
	}
	return dr, nil
}

func (d *Drafts) create(ctx context.Context, in CreateInput) (Draft, error) {
	if existing, err := d.load(ctx, in.ID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Draft{}, err
	}

	f, ok := d.features[in.Feature]
	if !ok {
		return Draft{}, fmt.Errorf("pipeline: unknown feature %q", in.Feature)
	}
	user, err := d.st.Q.GetUser(ctx, pgtype.UUID{Bytes: in.UserID, Valid: true})
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: load user: %w", err)
	}
	sets, _, err := d.warm(ctx, in.UserID, in.CompanyID, in.ProjectID, f)
	if err != nil {
		return Draft{}, err
	}
	project, err := d.lookups.Project(ctx, in.UserID, in.CompanyID, in.ProjectID)
	if err != nil {
		return Draft{}, err
	}
	submitted, err := d.st.Q.CountSubmittedDrafts(ctx, pgtype.UUID{Bytes: in.UserID, Valid: true})
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: count submitted drafts: %w", err)
	}
	loc := RailsZone(project.TimeZone)
	projectName := project.DisplayName
	if projectName == "" {
		projectName = project.Name
	}
	ectx := ExtractContext{
		ProjectName:    projectName,
		TimeZone:       loc.String(),
		Today:          d.now(),
		UserName:       user.Name,
		UserProcoreID:  user.ProcoreUserID,
		SubmittedCount: int(submitted),
		Transcript:     in.Transcript,
	}

	prompt := f.Prompt()
	req := llm.Request{
		System:       prompt.Text,
		User:         f.UserMessage(ectx, in.Transcript),
		Tools:        tools(f, sets),
		FinalTool:    FinalTool,
		MaxToolCalls: maxToolCalls,
	}
	start := time.Now()
	res, err := d.extractor.Extract(ctx, req, toolHandler(sets))
	elapsed := time.Since(start)
	if err != nil {
		d.log.Warn("extraction failed", "draft_id", in.ID, "feature", f.Name(), "turns", len(res.Turns), "error", err)
		return Draft{}, fmt.Errorf("%w: %w", ErrExtractFailed, err)
	}

	final, err := decodeFinal(res.Final)
	if err != nil {
		return Draft{}, fmt.Errorf("%w: decode %s input: %w", ErrExtractFailed, FinalTool, err)
	}
	payload, unresolved := f.Normalize(final.Payload, final.Unresolved)
	payload, added := f.ApplyDefaults(ectx, payload, unresolved, sets)
	unresolved = append(unresolved, added...)
	payload, unresolved = Validate(f, payload, unresolved, sets, ectx.Today, loc)
	unresolved = markBlocking(f, unresolved)

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: encode payload: %w", err)
	}
	unresolvedJSON, err := json.Marshal(unresolved)
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: encode unresolved: %w", err)
	}
	confidenceJSON, err := json.Marshal(final.Confidence)
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: encode confidence: %w", err)
	}
	model := res.Model
	if model == "" {
		model = d.model
	}
	extractMS := int(elapsed.Milliseconds())

	var row db.Draft
	err = d.st.WithTx(ctx, func(q *db.Queries) error {
		var err error
		row, err = q.InsertDraft(ctx, db.InsertDraftParams{
			ID:            pgtype.UUID{Bytes: in.ID, Valid: true},
			CompanyID:     in.CompanyID,
			ProjectID:     in.ProjectID,
			UserID:        pgtype.UUID{Bytes: in.UserID, Valid: true},
			Feature:       f.Name(),
			Status:        StatusExtracted,
			Transcript:    in.Transcript,
			CapturedAt:    pgtype.Timestamptz{Time: in.CapturedAt, Valid: true},
			LlmOutput:     payloadJSON,
			Unresolved:    unresolvedJSON,
			Confidence:    confidenceJSON,
			ModelVersion:  pgtype.Text{String: model, Valid: model != ""},
			PromptVersion: pgtype.Text{String: prompt.Version, Valid: prompt.Version != ""},
			ExtractMs:     pgtype.Int4{Int32: int32(extractMS), Valid: true},
		})
		if err != nil {
			return err
		}
		if err := d.insertManifest(ctx, q, in); err != nil {
			return err
		}
		return insertAudit(ctx, q, in.CompanyID, in.ID, AuditExtracted, newExtractedAudit(res), extractMS)
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "drafts_pkey" {
		// Another process stored this ID first; theirs wins.
		return d.load(ctx, in.ID)
	}
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: store draft: %w", err)
	}
	return d.toDraft(ctx, row)
}

// Get returns the user's draft.
func (d *Drafts) Get(ctx context.Context, userID, id uuid.UUID) (Draft, error) {
	row, err := d.st.Q.GetDraftForUser(ctx, db.GetDraftForUserParams{
		ID: pgtype.UUID{Bytes: id, Valid: true}, UserID: pgtype.UUID{Bytes: userID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: load draft: %w", err)
	}
	return d.toDraft(ctx, row)
}

// GetByJob returns the user's draft whose current submit job is jobID, or
// ErrNotFound.
func (d *Drafts) GetByJob(ctx context.Context, userID uuid.UUID, jobID int64) (Draft, error) {
	row, err := d.st.Q.GetDraftByJob(ctx, db.GetDraftByJobParams{
		UserID: pgtype.UUID{Bytes: userID, Valid: true}, JobID: pgtype.Int8{Int64: jobID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: load draft by job: %w", err)
	}
	return d.toDraft(ctx, row)
}

// SaveEdits stores the reviewer's payload after validating its IDs and dates
// against the lookups again. The draft becomes reviewed and loses any failure
// error. Its unresolved list becomes the validation result plus the earlier
// entries for fields still empty, and an edited audit event records the
// payload before and after. A submitting or submitted draft is ErrDraftLocked.
func (d *Drafts) SaveEdits(ctx context.Context, userID, id uuid.UUID, payload json.RawMessage) (Draft, error) {
	cur, err := d.Get(ctx, userID, id)
	if err != nil {
		return Draft{}, err
	}
	if cur.Status == StatusSubmitting || cur.Status == StatusSubmitted {
		return Draft{}, ErrDraftLocked
	}
	var edited map[string]any
	if err := json.Unmarshal(payload, &edited); err != nil {
		return Draft{}, fmt.Errorf("pipeline: decode payload: %w", err)
	}
	// Earlier flags for fields the reviewer left empty are kept, so a flag
	// only the model or a Procore 422 raised is not lost; the second
	// Validate pass inside revalidate drops unknown fields and stale
	// candidates from the carried-over entries.
	validated, unresolved, err := d.revalidate(ctx, userID, cur, edited)
	if err != nil {
		return Draft{}, err
	}
	saved, err := d.storeEdits(ctx, cur, validated, unresolved, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrDraftLocked
	}
	return saved, err
}

// storeEdits stores a revalidated payload and unresolved list on cur as
// SaveEdits does: status reviewed, error cleared, and an edited audit event
// with the payload before and after. pgx.ErrNoRows means the draft is being
// or has been submitted, or, with ifUnchanged, was updated since cur was
// read.
func (d *Drafts) storeEdits(ctx context.Context, cur Draft, payload map[string]any, unresolved []Unresolved, ifUnchanged bool) (Draft, error) {
	after, err := json.Marshal(payload)
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: encode payload: %w", err)
	}
	unresolvedJSON, err := json.Marshal(unresolved)
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: encode unresolved: %w", err)
	}
	var row db.Draft
	err = d.st.WithTx(ctx, func(q *db.Queries) error {
		var err error
		row, err = q.SaveDraftEdits(ctx, db.SaveDraftEditsParams{
			ID:          pgtype.UUID{Bytes: cur.ID, Valid: true},
			UserPayload: after,
			Unresolved:  unresolvedJSON,
			IfUpdatedAt: pgtype.Timestamptz{Time: cur.UpdatedAt, Valid: ifUnchanged},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err != nil {
			return fmt.Errorf("pipeline: save draft: %w", err)
		}
		return insertAudit(ctx, q, cur.CompanyID, cur.ID, AuditEdited, editedAudit{Before: cur.Payload(), After: after}, 0)
	})
	if err != nil {
		return Draft{}, err
	}
	return d.toDraft(ctx, row)
}

// warm loads f's lookup kinds for one extraction or validation. A fatal
// error (see LookupErrorFatal) is returned. A kind that failed otherwise,
// with no stale copy to serve, is logged and returned as an unavailable set
// for this call only, so its tool is left out; its error is returned in
// failed so a caller can refuse to judge IDs of that kind.
func (d *Drafts) warm(ctx context.Context, userID uuid.UUID, companyID, projectID int64, f Feature) (sets map[LookupKind]LookupSet, failed map[LookupKind]error, err error) {
	sets, failed = d.lookups.Warm(ctx, userID, companyID, projectID, f.LookupKinds())
	for _, k := range f.LookupKinds() {
		kerr, ok := failed[k]
		if !ok {
			continue
		}
		if LookupErrorFatal(kerr) {
			return nil, nil, kerr
		}
	}
	for k, kerr := range failed {
		d.log.Warn("lookup unavailable for this call", "kind", k, "company_id", companyID, "project_id", projectID, "error", kerr)
		sets[k] = LookupSet{Kind: k, Rows: []LookupRow{}, Unavailable: true, Reason: "could not be loaded from Procore"}
	}
	return sets, failed, nil
}

// finalInput is the decoded submit_draft tool input.
type finalInput struct {
	Payload    map[string]any
	Unresolved []Unresolved
	Confidence map[string]float64
}

// decodeFinal reads the submit_draft tool input leniently, since the API
// does not enforce the tool's schema: a payload that is not an object is
// empty, an unresolved entry that does not decode is skipped, and a
// confidence that is not a number is dropped (numbers are clamped to 0..1).
// Only input that is not a JSON object is an error.
func decodeFinal(raw json.RawMessage) (finalInput, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return finalInput{}, err
	}
	out := finalInput{Payload: map[string]any{}, Unresolved: []Unresolved{}, Confidence: map[string]float64{}}
	if json.Unmarshal(top["payload"], &out.Payload) != nil || out.Payload == nil {
		out.Payload = map[string]any{}
	}
	var items []json.RawMessage
	_ = json.Unmarshal(top["unresolved"], &items)
	for _, item := range items {
		var u Unresolved
		if json.Unmarshal(item, &u) == nil && u.Field != "" {
			out.Unresolved = append(out.Unresolved, u)
		}
	}
	var conf map[string]any
	_ = json.Unmarshal(top["confidence"], &conf)
	for k, v := range conf {
		if n, ok := v.(float64); ok {
			out.Confidence[k] = min(max(n, 0), 1)
		}
	}
	return out, nil
}

// isEmptyValue reports whether a payload value counts as unset: absent or
// null, a blank string, or an empty array.
func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	return false
}

// sameJSON reports whether two decoded payload values encode to the same
// JSON. Absent and null are the same; object keys are compared sorted.
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// List returns the user's drafts in a project, newest first. limit is
// clamped to 1..50.
func (d *Drafts) List(ctx context.Context, userID uuid.UUID, projectID int64, limit int) ([]Draft, error) {
	if limit < 1 || limit > maxListDrafts {
		limit = maxListDrafts
	}
	rows, err := d.st.Q.ListDraftsForUserProject(ctx, db.ListDraftsForUserProjectParams{
		UserID: pgtype.UUID{Bytes: userID, Valid: true}, ProjectID: projectID, Limit: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("pipeline: list drafts: %w", err)
	}
	out := make([]Draft, 0, len(rows))
	for _, r := range rows {
		dr, err := d.toDraft(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, dr)
	}
	return out, nil
}

// load reads a draft by ID whoever owns it.
func (d *Drafts) load(ctx context.Context, id uuid.UUID) (Draft, error) {
	row, err := d.st.Q.GetDraft(ctx, pgtype.UUID{Bytes: id, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, fmt.Errorf("pipeline: load draft: %w", err)
	}
	return d.toDraft(ctx, row)
}

// toDraft is fromRow with each unresolved entry's Blocking recomputed for
// the draft's feature, so a row stored before Blocking existed, or under
// different RequiredFields, reads the same as a fresh one, and with the
// draft's attachments loaded.
func (d *Drafts) toDraft(ctx context.Context, r db.Draft) (Draft, error) {
	dr, err := fromRow(r)
	if err != nil {
		return Draft{}, err
	}
	dr.Unresolved = markBlocking(d.features[dr.Feature], dr.Unresolved)
	if dr.Attachments, err = d.attachments(ctx, dr.ID); err != nil {
		return Draft{}, err
	}
	return dr, nil
}

func fromRow(r db.Draft) (Draft, error) {
	d := Draft{
		ID:            uuid.UUID(r.ID.Bytes),
		CompanyID:     r.CompanyID,
		ProjectID:     r.ProjectID,
		UserID:        uuid.UUID(r.UserID.Bytes),
		Feature:       r.Feature,
		Status:        r.Status,
		Transcript:    r.Transcript,
		CapturedAt:    r.CapturedAt.Time,
		LLMOutput:     r.LlmOutput,
		UserPayload:   r.UserPayload,
		Unresolved:    []Unresolved{},
		Confidence:    map[string]float64{},
		ModelVersion:  r.ModelVersion.String,
		PromptVersion: r.PromptVersion.String,
		ExtractMS:     int(r.ExtractMs.Int32),
		JobID:         r.JobID.Int64,
		CreatedAt:     r.CreatedAt.Time,
		UpdatedAt:     r.UpdatedAt.Time,
	}
	if len(r.Unresolved) > 0 {
		if err := json.Unmarshal(r.Unresolved, &d.Unresolved); err != nil {
			return Draft{}, fmt.Errorf("pipeline: decode unresolved: %w", err)
		}
		if d.Unresolved == nil {
			d.Unresolved = []Unresolved{}
		}
	}
	if len(r.Confidence) > 0 {
		if err := json.Unmarshal(r.Confidence, &d.Confidence); err != nil {
			return Draft{}, fmt.Errorf("pipeline: decode confidence: %w", err)
		}
		if d.Confidence == nil {
			d.Confidence = map[string]float64{}
		}
	}
	if r.ProcoreRecordID.Valid {
		d.Result = &Result{ProcoreID: r.ProcoreRecordID.Int64, Number: r.ProcoreRecordNumber.String, URL: r.ProcoreUrl.String}
	}
	if r.ErrorCode.Valid {
		d.Error = &DraftError{Code: r.ErrorCode.String, Message: r.ErrorMessage.String}
	}
	return d, nil
}

// toolDescriptions say what each lookup tool searches.
var toolDescriptions = map[LookupKind]string{
	KindUsers:        "Search the project directory for people by name, role, company or email.",
	KindLocations:    "Search the project's locations (building, level, room, area).",
	KindSpecSections: "Search the project's specification sections by number or title.",
	KindCostCodes:    "Search the project's cost codes by code or name.",
	KindVendors:      "Search the project's vendors (companies) by name, trade or abbreviation.",
	KindDrawings:     "Search the project's drawings by sheet number, title or discipline.",
	KindRFIManagers:  "Search the people who may manage an RFI on this project by name or login.",
}

// tools offers a list_<kind> tool for every lookup the user can read, then
// the final tool.
func tools(f Feature, sets map[LookupKind]LookupSet) []llm.Tool {
	var out []llm.Tool
	for _, k := range f.LookupKinds() {
		set, ok := sets[k]
		if !ok || set.Unavailable {
			continue
		}
		out = append(out, llm.Tool{
			Name:        "list_" + string(k),
			Description: toolDescriptions[k] + fmt.Sprintf(" Returns at most %d matching rows and the total match count.", maxToolRows),
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Case-insensitive text to match. Omit to list the first rows.",
					},
				},
			},
		})
	}
	return append(out, llm.Tool{
		Name:        FinalTool,
		Description: "Submit the extracted draft. Call exactly once, last.",
		InputSchema: f.OutputSchema(),
	})
}

// toolHandler answers list_<kind> calls from the warmed lookup sets with
// {"rows": [...], "total": n}: rows whose label or any extra value contains
// the query, case-insensitively, capped at maxToolRows.
func toolHandler(sets map[LookupKind]LookupSet) llm.ToolHandler {
	return func(_ context.Context, call llm.ToolCall) (json.RawMessage, error) {
		name, isList := strings.CutPrefix(call.Name, "list_")
		kind, ok := ParseKind(name)
		set, have := sets[kind]
		if !isList || !ok || !have || set.Unavailable {
			return nil, fmt.Errorf("unknown tool %q", call.Name)
		}
		var in struct {
			Query string `json:"query"`
		}
		if len(call.Input) > 0 {
			if err := json.Unmarshal(call.Input, &in); err != nil {
				return nil, fmt.Errorf("invalid input: %v", err)
			}
		}
		q := strings.ToLower(strings.TrimSpace(in.Query))
		rows := []LookupRow{}
		total := 0
		for _, r := range set.Rows {
			if !rowMatches(r, q) {
				continue
			}
			total++
			if len(rows) < maxToolRows {
				rows = append(rows, r)
			}
		}
		return json.Marshal(struct {
			Rows  []LookupRow `json:"rows"`
			Total int         `json:"total"`
		}{rows, total})
	}
}

func rowMatches(r LookupRow, q string) bool {
	if q == "" || strings.Contains(strings.ToLower(r.Label), q) {
		return true
	}
	for _, v := range r.Extra {
		if strings.Contains(strings.ToLower(v), q) {
			return true
		}
	}
	return false
}
