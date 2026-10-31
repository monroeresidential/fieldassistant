//go:build evals

package evals

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/monroeresidential/fieldassistant/api/internal/blob"
	"github.com/monroeresidential/fieldassistant/api/internal/features/rfi"
	"github.com/monroeresidential/fieldassistant/api/internal/llm"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline/pipelinetest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// Thresholds that fail the run.
const (
	maxP95          = 8 * time.Second
	minExactMatch   = 0.8
	maxHallucinated = 0
	extractTimeout  = 60 * time.Second
	companyID       = int64(1)
	lookupTTL       = time.Hour
)

type evalTokens struct{}

func (evalTokens) AccessToken(context.Context, uuid.UUID) (string, error) { return "eval", nil }

// TestEvals runs every case through the real extraction against the model
// named by LLM_EXTRACT_MODEL, with lookups served from fixture rows written
// straight into lookup_cache, and fails on any hallucinated ID, a p95 latency
// over 8 s or an exact-match rate under 0.8.
func TestEvals(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_API_KEY", "LLM_EXTRACT_MODEL", "TEST_DATABASE_URL"} {
		if os.Getenv(name) == "" {
			t.Skipf("%s not set; skipping the RFI eval run", name)
		}
	}
	cases := loadCases(t)
	model := os.Getenv("LLM_EXTRACT_MODEL")
	st := storetest.New(t)
	ctx := context.Background()

	feature := rfi.New()
	fp := pipelinetest.NewFakeProcore()
	var clock time.Time
	now := func() time.Time { return clock }
	lookups := pipeline.NewLookups(st, fp, evalTokens{}, lookupTTL, now, nil)
	extractor := llm.NewClient(os.Getenv("ANTHROPIC_API_KEY"), model, "", extractTimeout)
	drafts := pipeline.NewDrafts(st, lookups, extractor, nil, evalTokens{}, blob.NewMemory(), map[string]pipeline.Feature{feature.Name(): feature}, model, extractTimeout, now, nil)

	type row struct {
		name string
		s    score
		ms   int
		err  error
	}
	var rows []row
	for i, c := range cases {
		// Each case gets its own project so its lookups, time zone and
		// project detail never leak into another case.
		projectID := int64(1000 + i)
		var err error
		if clock, err = c.now(); err != nil {
			t.Fatalf("%s: %v", c.File, err)
		}
		fp.Project = procore.ProjectDetail{ID: projectID, Name: "Eval Tower", TimeZone: c.TimeZone}
		user, err := st.Q.UpsertUserByProcoreID(ctx, db.UpsertUserByProcoreIDParams{
			ProcoreUserID: c.User.ProcoreID,
			Email:         strings.ToLower(strings.ReplaceAll(c.User.Name, " ", ".")) + "@example.com",
			Name:          c.User.Name,
		})
		if err != nil {
			t.Fatalf("%s: upsert user: %v", c.File, err)
		}
		seedLookups(t, ctx, st.Q, projectID, clock, c.Fixture)

		d, err := drafts.Create(ctx, pipeline.CreateInput{
			ID:         uuid.New(),
			UserID:     uuid.UUID(user.ID.Bytes),
			CompanyID:  companyID,
			ProjectID:  projectID,
			Feature:    feature.Name(),
			Transcript: c.Transcript,
			CapturedAt: clock,
		})
		if err != nil {
			t.Errorf("%s: extraction failed: %v", c.File, err)
			// Count the case's expected fields as all missed.
			s, _ := scoreDraft(c, feature.IDFields(), []byte(`{}`), nil)
			rows = append(rows, row{name: c.File, s: score{Expected: s.Expected}, err: err})
			continue
		}
		s, err := scoreDraft(c, feature.IDFields(), d.LLMOutput, d.Unresolved)
		if err != nil {
			t.Fatalf("%s: %v", c.File, err)
		}
		for _, m := range s.Misses {
			t.Logf("%s: miss %s", c.File, m)
		}
		if len(s.Hallucinated) > 0 {
			t.Logf("%s: hallucinated %v; payload %s", c.File, s.Hallucinated, d.LLMOutput)
		}
		rows = append(rows, row{name: c.File, s: s, ms: d.ExtractMS})
	}
	for _, m := range []string{"ListProjectUsers", "ListPotentialRFIManagers", "ListLocations", "ListSpecSections", "ListCostCodes", "ListVendors", "ListDrawings"} {
		if n := fp.CallCount(m); n > 0 {
			t.Errorf("%s reached Procore %d times; the fixture cache should serve every lookup", m, n)
		}
	}

	var matched, expected, hallucinated int
	var precisionSum float64
	var latencies []int
	t.Logf("%-34s %9s %13s %10s %7s", "case", "fields", "hallucinated", "precision", "ms")
	for _, r := range rows {
		status := ""
		if r.err != nil {
			status = "  extraction failed"
		}
		t.Logf("%-34s %4d/%-4d %13d %10.2f %7d%s", r.name, r.s.Matched, r.s.Expected, len(r.s.Hallucinated), r.s.Precision, r.ms, status)
		matched += r.s.Matched
		expected += r.s.Expected
		hallucinated += len(r.s.Hallucinated)
		precisionSum += r.s.Precision
		if r.err == nil {
			latencies = append(latencies, r.ms)
		}
	}
	rate := 0.0
	if expected > 0 {
		rate = float64(matched) / float64(expected)
	}
	meanPrecision := 0.0
	if len(rows) > 0 {
		meanPrecision = precisionSum / float64(len(rows))
	}
	p95 := time.Duration(percentile(latencies, 0.95)) * time.Millisecond
	t.Logf("model %s, prompt %s", model, feature.Prompt().Version)
	t.Logf("exact-match rate %.3f (%d/%d), hallucinated IDs %d, mean unresolved precision %.2f, p95 latency %s",
		rate, matched, expected, hallucinated, meanPrecision, p95)

	if hallucinated > maxHallucinated {
		t.Errorf("hallucinated IDs: %d, want %d", hallucinated, maxHallucinated)
	}
	if p95 > maxP95 {
		t.Errorf("p95 latency %s over %s", p95, maxP95)
	}
	if rate < minExactMatch {
		t.Errorf("exact-match rate %.3f under %.2f", rate, minExactMatch)
	}
}

// seedLookups writes the fixture into lookup_cache as fresh rows, so Lookups
// serves them without calling Procore.
func seedLookups(t *testing.T, ctx context.Context, q *db.Queries, projectID int64, at time.Time, fixture map[pipeline.LookupKind][]pipeline.LookupRow) {
	t.Helper()
	for _, kind := range pipeline.AllKinds {
		rows := fixture[kind]
		if rows == nil {
			rows = []pipeline.LookupRow{}
		}
		body, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		err = q.UpsertLookup(ctx, db.UpsertLookupParams{
			CompanyID: companyID,
			ProjectID: projectID,
			Kind:      string(kind),
			Etag:      `"eval"`,
			Body:      body,
			FetchedAt: pgtype.Timestamptz{Time: at, Valid: true},
		})
		if err != nil {
			t.Fatalf("seed %s: %v", kind, err)
		}
	}
}

// percentile is the nearest-rank percentile of ms.
func percentile(ms []int, p float64) int {
	if len(ms) == 0 {
		return 0
	}
	s := append([]int{}, ms...)
	sort.Ints(s)
	i := int(math.Ceil(p*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return s[i]
}
