package pipeline

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

// A project member whose own token is refused a kind does not get the rows
// another member cached for it: the shared row is served only after a probe
// with the member's own token succeeds.
func TestLookups_SharedRowNotServedWithoutKindVerification(t *testing.T) {
	l, fp, _, _, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	userA, userB := uuid.New(), uuid.New()
	tokens.setToken(userA, "tok-a")
	tokens.setToken(userB, "tok-b")
	fp.ErrForToken["tok-b"] = map[string]error{"ProbeKind": fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403, Body: "no directory access"})}
	ctx := context.Background()

	if a, err := l.Get(ctx, userA, testCompany, testProject, KindUsers, false); err != nil || len(a.Rows) != 2 {
		t.Fatalf("user A = %+v, %v; want 2 rows", a, err)
	}
	b, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false)
	if err != nil {
		t.Fatalf("user B: %v", err)
	}
	if !b.Unavailable || len(b.Rows) != 0 || b.Reason != "no directory access" {
		t.Fatalf("user B set = %+v, want unavailable with no rows", b)
	}
	// The denial is remembered for B: no second probe within the TTL.
	if again, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil || !again.Unavailable {
		t.Fatalf("user B again = %+v, %v", again, err)
	}
	if n := fp.CallCount("ProbeKind"); n != 1 {
		t.Errorf("ProbeKind calls = %d, want 1", n)
	}
	// A's own fetch counted as verification: A is never probed.
	for _, c := range fp.Calls() {
		if c.Method == "ProbeKind" && c.AccessToken == "tok-a" {
			t.Error("user A was probed after fetching the list")
		}
	}

	// A 404 on the probe is a denial too.
	userC := uuid.New()
	tokens.setToken(userC, "tok-c")
	fp.ErrForToken["tok-c"] = map[string]error{"ProbeKind": &procore.APIError{Status: 404, Body: "not found"}}
	if c, err := l.Get(ctx, userC, testCompany, testProject, KindUsers, false); err != nil || !c.Unavailable || len(c.Rows) != 0 {
		t.Fatalf("user C after a 404 probe = %+v, %v; want unavailable with no rows", c, err)
	}
}

// A probe that fails for a reason other than a denial does not serve the
// shared row to a user never verified for the kind: the error surfaces.
func TestLookups_ProbeError_UnverifiedUser_FailsClosed(t *testing.T) {
	l, fp, _, _, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	userA, userB := uuid.New(), uuid.New()
	tokens.setToken(userA, "tok-a")
	tokens.setToken(userB, "tok-b")
	ctx := context.Background()
	if _, err := l.Get(ctx, userA, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	for name, perr := range map[string]error{
		"network": fmt.Errorf("probe: %w", procore.ErrUnavailable),
		"5xx":     &procore.APIError{Status: 503, Body: "down"},
	} {
		fp.ErrForToken["tok-b"] = map[string]error{"ProbeKind": perr}
		set, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false)
		if err == nil {
			t.Errorf("%s: user B = %+v, want the probe error and no rows", name, set)
		}
		if len(set.Rows) != 0 {
			t.Errorf("%s: user B got %d shared rows", name, len(set.Rows))
		}
	}
	tokens.setErr(userB, errors.New("refresh failed"))
	if set, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); !errors.Is(err, ErrLookupToken) || len(set.Rows) != 0 {
		t.Errorf("token error: user B = %+v, %v; want ErrLookupToken and no rows", set, err)
	}
}

// A user verified before, whose verification has expired, is served the
// cached row on a transient probe failure; the same holds for the stale
// row after a failed full fetch. A never-verified user gets neither.
func TestLookups_ProbeError_PreviouslyVerified_ServesStale(t *testing.T) {
	l, fp, clock, _, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	userA, userB, userC := uuid.New(), uuid.New(), uuid.New()
	tokens.setToken(userA, "tok-a")
	tokens.setToken(userB, "tok-b")
	tokens.setToken(userC, "tok-c")
	ctx := context.Background()
	if _, err := l.Get(ctx, userA, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	if b, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil || len(b.Rows) != 2 {
		t.Fatalf("user B first = %+v, %v", b, err) // probe succeeds: B is verified
	}

	// B's verification expires; A refetches so the shared row is fresh.
	clock.Advance(testTTL + time.Second)
	if _, err := l.Get(ctx, userA, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	fp.ErrForToken["tok-b"] = map[string]error{"ProbeKind": fmt.Errorf("probe: %w", procore.ErrUnavailable)}
	if b, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil || len(b.Rows) != 2 {
		t.Fatalf("previously verified B after a failed probe = %+v, %v; want the cached rows", b, err)
	}

	// The shared row goes stale and every refetch fails: B (verified
	// before) gets the stale row, C (never verified) gets the error.
	clock.Advance(testTTL + time.Second)
	fp.Err["ListProjectUsers"] = fmt.Errorf("list: %w", procore.ErrUnavailable)
	if b, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil || len(b.Rows) != 2 {
		t.Fatalf("previously verified B after a failed fetch = %+v, %v; want the stale rows", b, err)
	}
	if c, err := l.Get(ctx, userC, testCompany, testProject, KindUsers, false); !errors.Is(err, procore.ErrUnavailable) || len(c.Rows) != 0 {
		t.Fatalf("never-verified C after a failed fetch = %+v, %v; want ErrUnavailable and no rows", c, err)
	}
}

// ProbeKind takes a LookupKind's string; the Procore client's names must match.
func TestLookupKinds_MatchProcoreKinds(t *testing.T) {
	want := map[LookupKind]string{
		KindUsers: procore.KindUsers, KindLocations: procore.KindLocations, KindSpecSections: procore.KindSpecSections,
		KindCostCodes: procore.KindCostCodes, KindVendors: procore.KindVendors, KindDrawings: procore.KindDrawings,
		KindRFIManagers: procore.KindRFIManagers,
	}
	if len(AllKinds) != len(want) {
		t.Errorf("AllKinds = %v, want the %d kinds above", AllKinds, len(want))
	}
	for _, k := range AllKinds {
		if string(k) != want[k] {
			t.Errorf("kind %q has Procore name %q", k, want[k])
		}
	}
}

// A successful probe is remembered per user and kind for the TTL.
func TestLookups_KindVerificationCachedForTTL(t *testing.T) {
	l, fp, clock, _, tokens := newLookupsWithTokens(t)
	fp.Users = twoUsers()
	fp.Locations = []procore.Location{{ID: 77, Name: "Grid C4"}}
	userA, userB := uuid.New(), uuid.New()
	tokens.setToken(userA, "tok-a")
	tokens.setToken(userB, "tok-b")
	ctx := context.Background()

	for _, k := range []LookupKind{KindUsers, KindLocations} {
		if _, err := l.Get(ctx, userA, testCompany, testProject, k, false); err != nil {
			t.Fatal(err)
		}
	}
	for range 3 {
		if set, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil || len(set.Rows) != 2 {
			t.Fatalf("user B = %+v, %v", set, err)
		}
	}
	probes := func() (users, locations int) {
		for _, c := range fp.Calls() {
			if c.Method == "ProbeKind" && c.AccessToken == "tok-b" {
				switch c.Kind {
				case "users":
					users++
				case "locations":
					locations++
				}
			}
		}
		return
	}
	if u, _ := probes(); u != 1 {
		t.Fatalf("users probes within the TTL = %d, want 1", u)
	}
	// Verification is per kind.
	if _, err := l.Get(ctx, userB, testCompany, testProject, KindLocations, false); err != nil {
		t.Fatal(err)
	}
	if _, loc := probes(); loc != 1 {
		t.Fatalf("locations probes = %d, want 1", loc)
	}

	// After the TTL the shared row is stale and B's Get refetches with B's
	// own token, which verifies B again without a probe.
	clock.Advance(testTTL + time.Second)
	if _, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, userB, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	if u, _ := probes(); u != 1 {
		t.Errorf("users probes after B's own refetch = %d, want still 1", u)
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
			if _, errs := l.Warm(ctx, outsider, testCompany, testProject, []LookupKind{KindUsers}); !errors.Is(errs[KindUsers], procore.ErrPermissionDenied) || !LookupErrorFatal(errs[KindUsers]) {
				t.Fatalf("Warm errs = %v, want a fatal ErrPermissionDenied for users", errs)
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
	// The stale row is served only to a user verified for the kind before;
	// TestLookups_ProbeError_PreviouslyVerified_ServesStale covers the rest.
	user := uuid.New()

	if _, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false); err != nil {
		t.Fatal(err)
	}
	clock.Advance(testTTL + time.Second)
	fp.Err["ListProjectUsers"] = procore.ErrUnavailable

	set, err := l.Get(ctx, user, testCompany, testProject, KindUsers, false)
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

	got, errs := l.Warm(context.Background(), uuid.New(), testCompany, testProject, []LookupKind{KindLocations, KindUsers})
	if len(errs) != 1 || !errors.Is(errs[KindLocations], procore.ErrUnavailable) || LookupErrorFatal(errs[KindLocations]) {
		t.Fatalf("errs = %v, want a non-fatal ErrUnavailable for locations only", errs)
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

func TestRowsFor_RFIManagers(t *testing.T) {
	rows := rowsFor(KindRFIManagers, []procore.RFIManager{{ID: 3, Name: "Pat Manager", Login: "pat@x.com"}, {ID: 4, Name: "Sam"}})
	want := []LookupRow{{ID: 3, Label: "Pat Manager", Extra: map[string]string{"login": "pat@x.com"}}, {ID: 4, Label: "Sam"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %+v, want %+v", rows, want)
	}
	if rows := rowsFor(KindUsers, []procore.RFIManager{{ID: 3}}); len(rows) != 0 {
		t.Errorf("managers under the users kind = %+v, want none", rows)
	}
}

// The rfi_managers kind is Procore's potential RFI managers list, cached
// and probed like any other kind.
func TestLookups_RFIManagers_FromPotentialManagers(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.RFIManagers = []procore.RFIManager{{ID: 42, Name: "Pat Field", Login: "pat@x.com"}}
	ctx := context.Background()

	set, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindRFIManagers, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if set.Unavailable || len(set.Rows) != 1 || set.Rows[0].ID != 42 || set.Rows[0].Label != "Pat Field" {
		t.Fatalf("set = %+v", set)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 1 {
		t.Errorf("ListPotentialRFIManagers called %d times, want 1", n)
	}

	// A second user reading the cached row is probed for the kind.
	if _, err := l.Get(ctx, uuid.New(), testCompany, testProject, KindRFIManagers, false); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	var probed bool
	for _, c := range fp.Calls() {
		probed = probed || (c.Method == "ProbeKind" && c.Kind == string(KindRFIManagers))
	}
	if !probed {
		t.Error("second user was not probed for rfi_managers")
	}
}

// A user who may not see the managers list (a 403, or the RFI tool off) gets
// an unavailable set, like any other kind.
func TestLookups_RFIManagers_Denied_Unavailable(t *testing.T) {
	l, fp, _, _ := newLookups(t)
	fp.Err["ListPotentialRFIManagers"] = &procore.APIError{Status: 404}
	set, err := l.Get(context.Background(), uuid.New(), testCompany, testProject, KindRFIManagers, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !set.Unavailable || len(set.Rows) != 0 {
		t.Fatalf("set = %+v, want unavailable", set)
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

// A kind whose Procore tool is off answers its list with a 404 and an empty
// body. That is a per-user denial like a 403, not a failure.
func TestLookups_Fetch404_Unavailable(t *testing.T) {
	l, fp, _, st := newLookups(t)
	fp.Err["ListSpecSections"] = &procore.APIError{Status: 404}
	ctx := context.Background()
	user := uuid.New()

	set, err := l.Get(ctx, user, testCompany, testProject, KindSpecSections, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !set.Unavailable || len(set.Rows) != 0 || set.Reason != "tool not enabled on this project" {
		t.Fatalf("set = %+v, want unavailable with the tool-off reason and no rows", set)
	}
	if _, err := st.Q.GetLookup(ctx, db.GetLookupParams{CompanyID: testCompany, ProjectID: testProject, Kind: string(KindSpecSections)}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("shared row err = %v, want no row written", err)
	}

	again, err := l.Get(ctx, user, testCompany, testProject, KindSpecSections, false)
	if err != nil || !again.Unavailable {
		t.Fatalf("second Get = %+v, %v; want the remembered denial", again, err)
	}
	if n := fp.CallCount("ListSpecSections"); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}
