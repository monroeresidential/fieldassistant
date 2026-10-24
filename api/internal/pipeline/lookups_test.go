package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/monroeresidential/fieldassistant/api/internal/auth"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline/pipelinetest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

// testTokens returns "tok" for every user unless a per-user token or error is set.
type testTokens struct {
	mu     sync.Mutex
	tokens map[uuid.UUID]string
	errs   map[uuid.UUID]error
}

func newTestTokens() *testTokens {
	return &testTokens{tokens: map[uuid.UUID]string{}, errs: map[uuid.UUID]error{}}
}

func (t *testTokens) AccessToken(_ context.Context, id uuid.UUID) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.errs[id]; err != nil {
		return "", err
	}
	if tok, ok := t.tokens[id]; ok {
		return tok, nil
	}
	return "tok", nil
}

func (t *testTokens) setToken(id uuid.UUID, tok string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tokens[id] = tok
}

func (t *testTokens) setErr(id uuid.UUID, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.errs[id] = err
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

const (
	testCompany = int64(10)
	testProject = int64(20)
	testTTL     = 10 * time.Minute
)

func newLookups(t *testing.T) (*Lookups, *pipelinetest.FakeProcore, *testClock, *store.Store) {
	t.Helper()
	l, fp, clock, st, _ := newLookupsWithTokens(t)
	return l, fp, clock, st
}

func newLookupsWithTokens(t *testing.T) (*Lookups, *pipelinetest.FakeProcore, *testClock, *store.Store, *testTokens) {
	t.Helper()
	st := storetest.New(t)
	fp := pipelinetest.NewFakeProcore()
	clock := &testClock{t: time.Now().UTC().Truncate(time.Second)}
	tokens := newTestTokens()
	return NewLookups(st, fp, tokens, testTTL, clock.Now, nil), fp, clock, st, tokens
}

func twoUsers() []procore.ProjectUser {
	return []procore.ProjectUser{{ID: 1, Name: "Ann"}, {ID: 2, Name: "Ben"}}
}

func TestLookups_FetchesThenServesCache(t *testing.T) {
	l, fp, clock, _ := newLookups(t)
	fp.Users = twoUsers()
	ctx := context.Background()
	user := uuid.New()

	set, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false)
	if err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if len(set.Rows) != 2 || !set.Has(1) || !set.Has(2) || set.Has(3) {
		t.Fatalf("rows = %+v", set.Rows)
	}
	if n := fp.CallCount("ListProjectUsers"); n != 1 {
		t.Fatalf("calls after first Get = %d, want 1", n)
	}

	if _, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if n := fp.CallCount("ListProjectUsers"); n != 1 {
		t.Fatalf("calls within ttl = %d, want 1", n)
	}

	clock.Advance(testTTL + time.Second)
	if _, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatalf("third Get: %v", err)
	}
	if n := fp.CallCount("ListProjectUsers"); n != 2 {
		t.Fatalf("calls after ttl = %d, want 2", n)
	}
}

func TestLookups_Force_BypassesTTL(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.Users = twoUsers()
	ctx := context.Background()

	if _, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, true); err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("ListProjectUsers"); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

func TestLookups_Forbidden_CachedUnavailable(t *testing.T) {
	l, fp, _, st := newLookups(t)
	fp.Err["ListCostCodes"] = fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403, Body: "no budget access"})
	ctx := context.Background()
	user := uuid.New()

	set, err := l.Get(ctx, user, testCompany, testProject, KindCostCodes, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !set.Unavailable || len(set.Rows) != 0 || set.Reason != "no budget access" {
		t.Fatalf("set = %+v, want unavailable with reason and no rows", set)
	}
	if _, err := st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: testCompany, ProjectID: testProject, Kind: string(KindCostCodes)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("shared row err = %v, want no row written for a per-user denial", err)
	}

	again, err := l.Get(ctx, user, testCompany, testProject, KindCostCodes, false)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !again.Unavailable {
		t.Error("remembered denial lost Unavailable")
	}
	if n := fp.CallCount("ListCostCodes"); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func TestLookups_Forbidden_PerUserOnly(t *testing.T) {
	l, fp, _, st, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	userA, userB := uuid.New(), uuid.New()
	tokens.setToken(userA, "tok-a")
	tokens.setToken(userB, "tok-b")
	fp.ErrForToken["tok-a"] = map[string]error{"ListProjectUsers": procore.ErrPermissionDenied}
	ctx := context.Background()

	a, err := l.Get(ctx, userA, testCompany, testProject, KindUsers, false)
	if err != nil {
		t.Fatalf("user A: %v", err)
	}
	if !a.Unavailable {
		t.Fatalf("user A set = %+v, want unavailable", a)
	}

	b, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false)
	if err != nil {
		t.Fatalf("user B: %v", err)
	}
	if b.Unavailable || len(b.Rows) != 2 {
		t.Fatalf("user B set = %+v, want 2 rows", b)
	}
	row, err := st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: testCompany, ProjectID: testProject, Kind: string(KindUsers)})
	if err != nil {
		t.Fatalf("GetLookup: %v", err)
	}
	if row.Unavailable {
		t.Error("shared row marked unavailable by one user's denial")
	}
}

func TestLookups_Forbidden_KeepsExistingSharedRows(t *testing.T) {
	l, fp, clock, st, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	userA, userB := uuid.New(), uuid.New()
	tokens.setToken(userA, "tok-a")
	ctx := context.Background()

	if _, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	clock.Advance(testTTL + time.Second)
	fp.ErrForToken["tok-a"] = map[string]error{"ListProjectUsers": procore.ErrPermissionDenied}
	if _, err := l.Get(ctx, userA, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	row, err := st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: testCompany, ProjectID: testProject, Kind: string(KindUsers)})
	if err != nil {
		t.Fatal(err)
	}
	if row.Unavailable || string(row.Body) == "[]" {
		t.Errorf("shared row clobbered: unavailable=%v body=%s", row.Unavailable, row.Body)
	}
}

func TestLookups_UserNotOnProject_PermissionDenied(t *testing.T) {
	for name, denied := range map[string]error{
		"403": fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403}),
		"404": &procore.APIError{Status: 404},
	} {
		t.Run(name, func(t *testing.T) {
			l, fp, _, st, tokens := newLookupsWithTokens(t)
			fp.Users = twoUsers()
			outsider := uuid.New()
			tokens.setToken(outsider, "tok-out")
			fp.ErrForToken["tok-out"] = map[string]error{"GetProject": denied}
			ctx := context.Background()

			if _, err := l.Get(ctx, outsider, testCompany, testProject, KindUsers, false); !errors.Is(err, procore.ErrPermissionDenied) {
				t.Fatalf("Get err = %v, want ErrPermissionDenied", err)
			}
			if _, err := l.Project(ctx, outsider, testCompany, testProject); !errors.Is(err, procore.ErrPermissionDenied) {
				t.Fatalf("Project err = %v, want ErrPermissionDenied", err)
			}
			if _, err := l.Warm(ctx, outsider, testCompany, testProject, []LookupKind{KindUsers}); !errors.Is(err, procore.ErrPermissionDenied) {
				t.Fatalf("Warm err = %v, want ErrPermissionDenied", err)
			}
			if n := fp.CallCount("ListProjectUsers"); n != 0 {
				t.Errorf("list calls = %d, want 0", n)
			}
			if _, err := st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: testCompany, ProjectID: testProject, Kind: string(KindUsers)}); !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("row written for outsider: %v", err)
			}
		})
	}
}

func TestLookups_CachedRowNotServedToUserWithoutAccess(t *testing.T) {
	l, fp, _, _, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	member, outsider := uuid.New(), uuid.New()
	tokens.setToken(outsider, "tok-out")
	fp.ErrForToken["tok-out"] = map[string]error{"GetProject": procore.ErrPermissionDenied}
	ctx := context.Background()

	if _, err := l.Get(ctx, member, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, outsider, testCompany, testProject, KindUsers, false); !errors.Is(err, procore.ErrPermissionDenied) {
		t.Fatalf("outsider err = %v, want ErrPermissionDenied despite warm cache", err)
	}
}

func TestLookups_AccessAndProjectShareOneCall(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.Users = twoUsers()
	ctx := context.Background()
	user := uuid.New()

	if _, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Project(ctx, user, testCompany, testProject); err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("GetProject"); n != 1 {
		t.Fatalf("GetProject calls = %d, want 1", n)
	}
}

// The project-access check stays fresh (cached) so the failure comes from the
// list refresh, which must surface rather than serve the stale copy.
func TestLookups_RefreshTokenError_NotServedStale(t *testing.T) {
	cases := map[string]struct {
		want   error
		break_ func(tokens *testTokens, fp *pipelinetest.FakeProcore, user uuid.UUID)
	}{
		"reauth": {auth.ErrReauthRequired, func(tokens *testTokens, _ *pipelinetest.FakeProcore, user uuid.UUID) {
			tokens.setErr(user, auth.ErrReauthRequired)
		}},
		"unauthorized": {procore.ErrUnauthorized, func(_ *testTokens, fp *pipelinetest.FakeProcore, _ uuid.UUID) {
			fp.Err["ListProjectUsers"] = procore.ErrUnauthorized
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			l, fp, clock, _, tokens := newLookupsWithTokens(t)
			fp.Users = twoUsers()
			ctx := context.Background()
			user := uuid.New()

			if _, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false); err != nil {
				t.Fatal(err)
			}
			clock.Advance(testTTL - time.Second)
			c.break_(tokens, fp, user)

			if _, err := l.Get(ctx, user, testCompany, testProject, KindUsers, true); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestLookups_RefreshFailure_ServesStale(t *testing.T) {
	l, fp, clock, _ := newLookups(t)
	fp.Users = twoUsers()
	ctx := context.Background()

	if _, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	clock.Advance(testTTL + time.Second)
	fp.Err["ListProjectUsers"] = procore.ErrUnavailable

	set, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, false)
	if err != nil {
		t.Fatalf("Get with stale row: %v", err)
	}
	if len(set.Rows) != 2 {
		t.Fatalf("rows = %d, want stale 2", len(set.Rows))
	}
}

func TestLookups_NoRowAndProcoreDown_ReturnsError(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.Err["ListProjectUsers"] = procore.ErrUnavailable

	_, err := l.Get(context.Background(), uuid.New(), testCompany, testProject, KindUsers, false)
	if !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

func TestLookups_Warm_ContinuesAfterError(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.Users = twoUsers()
	fp.Err["ListLocations"] = procore.ErrUnavailable

	got, err := l.Warm(context.Background(), uuid.New(), testCompany, testProject, []LookupKind{KindLocations, KindUsers})
	if !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, ok := got[KindUsers]; !ok || len(got[KindUsers].Rows) != 2 {
		t.Fatalf("users not warmed after locations failed: %+v", got)
	}
	if _, ok := got[KindLocations]; ok {
		t.Error("failed kind should be absent from result")
	}
}

func TestLookups_Project_CachedForTTL(t *testing.T) {
	l, fp, clock, _ := newLookups(t)
	fp.Project = procore.ProjectDetail{ID: testProject, Name: "Tower", TimeZone: "America/Chicago"}
	ctx := context.Background()
	user := uuid.New()

	for i := 0; i < 2; i++ {
		p, err := l.Project(ctx, user, testCompany, testProject)
		if err != nil {
			t.Fatal(err)
		}
		if p.TimeZone != "America/Chicago" {
			t.Fatalf("project = %+v", p)
		}
	}
	if n := fp.CallCount("GetProject"); n != 1 {
		t.Fatalf("calls within ttl = %d, want 1", n)
	}
	clock.Advance(testTTL + time.Second)
	if _, err := l.Project(ctx, user, testCompany, testProject); err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("GetProject"); n != 2 {
		t.Fatalf("calls after ttl = %d, want 2", n)
	}
}

func TestLookups_ETagStableForSameRows(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.Users = twoUsers()
	ctx := context.Background()

	a, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, true)
	if err != nil {
		t.Fatal(err)
	}
	if a.ETag == "" || a.ETag != b.ETag {
		t.Fatalf("etags %q and %q, want equal and non-empty", a.ETag, b.ETag)
	}
	if len(a.ETag) != 18 || a.ETag[0] != '"' || a.ETag[17] != '"' {
		t.Fatalf("etag %q, want quoted 16 hex chars", a.ETag)
	}

	fp.Users = append(twoUsers(), procore.ProjectUser{ID: 3, Name: "Cy"})
	c, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindUsers, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.ETag == a.ETag {
		t.Error("etag unchanged after rows changed")
	}
}

func TestRowsFor_Users_LabelAndExtra(t *testing.T) {
	u := procore.ProjectUser{ID: 7, Name: "Ben West", JobTitle: "Architect", EmailAddress: "ben@west.example"}
	u.Vendor.Name = "West Studio"

	rows := rowsFor(KindUsers, []procore.ProjectUser{u})
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	if r.ID != 7 || r.Label != "Ben West" {
		t.Errorf("row = %+v", r)
	}
	want := map[string]string{"role": "Architect", "company": "West Studio", "email": "ben@west.example"}
	for k, v := range want {
		if r.Extra[k] != v {
			t.Errorf("extra[%s] = %q, want %q", k, r.Extra[k], v)
		}
	}
}

func TestRowsFor_OtherKinds(t *testing.T) {
	cases := []struct {
		kind      LookupKind
		items     any
		label     string
		extraKey  string
		extraWant string
	}{
		{KindLocations, []procore.Location{{ID: 1, Name: "Level 2/Corridor", NodeName: "Corridor"}}, "Level 2/Corridor", "node", "Corridor"},
		{KindSpecSections, []procore.SpecSection{{ID: 1, Number: "03 30 00", Description: "Cast-in-Place Concrete"}}, "03 30 00 Cast-in-Place Concrete", "number", "03 30 00"},
		{KindCostCodes, []procore.CostCode{{ID: 1, FullCode: "03-300", Name: "Concrete"}}, "03-300 Concrete", "code", "03-300"},
		{KindVendors, []procore.Vendor{{ID: 1, Name: "Acme", TradeName: "Electrical", AbbreviatedName: "ACM"}}, "Acme", "trade", "Electrical"},
		{KindDrawings, []procore.Drawing{{ID: 1, Number: "A-101", Title: "Floor Plan", Discipline: "Architectural"}}, "A-101 Floor Plan", "discipline", "Architectural"},
	}
	for _, c := range cases {
		rows := rowsFor(c.kind, c.items)
		if len(rows) != 1 || rows[0].Label != c.label || rows[0].Extra[c.extraKey] != c.extraWant {
			t.Errorf("%s: rows = %+v, want label %q extra[%s]=%q", c.kind, rows, c.label, c.extraKey, c.extraWant)
		}
	}
}

func TestParseKind(t *testing.T) {
	for _, k := range AllKinds {
		got, ok := ParseKind(string(k))
		if !ok || got != k {
			t.Errorf("ParseKind(%q) = %q, %v", k, got, ok)
		}
	}
	if _, ok := ParseKind("people"); ok {
		t.Error("ParseKind accepted unknown kind")
	}
}
