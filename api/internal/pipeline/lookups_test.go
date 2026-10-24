package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline/pipelinetest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store"
	"github.com/monroeresidential/fieldassistant/api/internal/store/db"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

type fixedTokens struct{}

func (fixedTokens) AccessToken(context.Context, uuid.UUID) (string, error) { return "tok", nil }

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
	st := storetest.New(t)
	fp := pipelinetest.NewFakeProcore()
	clock := &testClock{t: time.Now().UTC().Truncate(time.Second)}
	return NewLookups(st, fp, fixedTokens{}, testTTL, clock.Now, nil), fp, clock, st
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

	set, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindCostCodes, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !set.Unavailable || len(set.Rows) != 0 || set.Reason == "" {
		t.Fatalf("set = %+v, want unavailable with reason and no rows", set)
	}
	row, err := st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: testCompany, ProjectID: testProject, Kind: string(KindCostCodes)})
	if err != nil {
		t.Fatalf("GetLookup: %v", err)
	}
	if !row.Unavailable {
		t.Error("stored row unavailable = false, want true")
	}

	again, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindCostCodes, false)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if !again.Unavailable {
		t.Error("cached set lost Unavailable")
	}
	if n := fp.CallCount("ListCostCodes"); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
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

	for i := 0; i < 2; i++ {
		p, err := l.Project(ctx, uuid.New(), testCompany, testProject)
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
	if _, err := l.Project(ctx, uuid.New(), testCompany, testProject); err != nil {
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
