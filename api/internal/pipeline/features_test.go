package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/monroeresidential/fieldassistant/api/internal/pipeline/pipelinetest"
	"github.com/monroeresidential/fieldassistant/api/internal/procore"
	"github.com/monroeresidential/fieldassistant/api/internal/store/storetest"
)

func newFeatures(t *testing.T, probe FeatureProbe) (*Features, *pipelinetest.FakeProcore, *testClock) {
	t.Helper()
	st := storetest.New(t)
	fp := pipelinetest.NewFakeProcore()
	clock := &testClock{t: time.Now().UTC().Truncate(time.Second)}
	tokens := newTestTokens()
	lookups := NewLookups(st, fp, tokens, testTTL, clock.Now, nil)
	return NewFeatures(lookups, fp, tokens, probe, testTTL, clock.Now, nil), fp, clock
}

// rfiState returns the rfi feature's state from states, failing the test
// when it is missing.
func rfiState(t *testing.T, states []FeatureState) FeatureState {
	t.Helper()
	for _, s := range states {
		if s.Feature == "rfi" {
			return s
		}
	}
	t.Fatalf("no rfi feature in %+v", states)
	return FeatureState{}
}

func forbidden(body string) error {
	return fmt.Errorf("%w: %w", procore.ErrPermissionDenied, &procore.APIError{Status: 403, Body: body})
}

func TestFeatures_StandardUser_RFIAvailable(t *testing.T) {
	f, fp, clock := newFeatures(t, ProbeRFIManagers)
	fp.RFIManagers = []procore.RFIManager{{ID: 1, Name: "Ann", Login: "ann@x.com"}}

	states, at, err := f.Get(context.Background(), uuid.New(), testCompany, testProject, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := rfiState(t, states); !got.Available || got.Reason != "" {
		t.Fatalf("rfi = %+v, want available with no reason", got)
	}
	if !at.Equal(clock.Now()) {
		t.Errorf("fetched at %v, want %v", at, clock.Now())
	}
	calls := fp.Calls()
	if calls[0].Method != "GetProject" {
		t.Errorf("first call %q, want the project visibility check first", calls[0].Method)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 1 {
		t.Fatalf("ListPotentialRFIManagers calls = %d, want 1", n)
	}
	for _, c := range calls {
		if c.Method == "ListPotentialRFIManagers" && (c.CompanyID != testCompany || c.ProjectID != testProject || c.AccessToken != "tok") {
			t.Errorf("call %+v, want company, project and the user's token", c)
		}
	}
}

func TestFeatures_ReadOnlyUser_RFIUnavailableWithReason(t *testing.T) {
	f, fp, _ := newFeatures(t, ProbeRFIManagers)
	fp.RFIManagersErr = forbidden(`{"errors":["Permission denied"]}`)

	states, _, err := f.Get(context.Background(), uuid.New(), testCompany, testProject, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := rfiState(t, states); got.Available || got.Reason != "Permission denied" {
		t.Fatalf("rfi = %+v, want unavailable with reason %q", got, "Permission denied")
	}
}

func TestFeatures_ToolOff_404_Unavailable(t *testing.T) {
	f, fp, _ := newFeatures(t, ProbeRFIManagers)
	fp.RFIManagersErr = &procore.APIError{Status: 404, Body: `{"errors":"not found"}`}

	states, _, err := f.Get(context.Background(), uuid.New(), testCompany, testProject, false)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := rfiState(t, states); got.Available || got.Reason != "RFI tool not enabled on this project" {
		t.Fatalf("rfi = %+v, want unavailable, tool not enabled", got)
	}
}

func TestFeatures_CachedForTTL_ThenRefetched(t *testing.T) {
	f, fp, clock := newFeatures(t, ProbeRFIManagers)
	ctx := context.Background()
	user := uuid.New()

	if _, _, err := f.Get(ctx, user, testCompany, testProject, false); err != nil {
		t.Fatal(err)
	}
	clock.Advance(testTTL - time.Second)
	if _, _, err := f.Get(ctx, user, testCompany, testProject, false); err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 1 {
		t.Fatalf("calls within ttl = %d, want 1", n)
	}

	clock.Advance(2 * time.Second)
	if _, _, err := f.Get(ctx, user, testCompany, testProject, false); err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 2 {
		t.Fatalf("calls after ttl = %d, want 2", n)
	}

	// Another user does not share the first user's answer.
	if _, _, err := f.Get(ctx, uuid.New(), testCompany, testProject, false); err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 3 {
		t.Fatalf("calls after a second user = %d, want 3", n)
	}
}

func TestFeatures_ForceRefetches(t *testing.T) {
	f, fp, _ := newFeatures(t, ProbeRFIManagers)
	ctx := context.Background()
	user := uuid.New()

	if _, _, err := f.Get(ctx, user, testCompany, testProject, false); err != nil {
		t.Fatal(err)
	}
	fp.RFIManagersErr = forbidden(`{"errors":["Permission denied"]}`)
	states, _, err := f.Get(ctx, user, testCompany, testProject, true)
	if err != nil {
		t.Fatal(err)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	if got := rfiState(t, states); got.Available {
		t.Fatalf("rfi = %+v, want the forced refetch's answer", got)
	}
}

func TestFeatures_InvalidateDropsCache(t *testing.T) {
	f, fp, _ := newFeatures(t, ProbeRFIManagers)
	ctx := context.Background()
	user, other := uuid.New(), uuid.New()

	for _, u := range []uuid.UUID{user, other} {
		if _, _, err := f.Get(ctx, u, testCompany, testProject, false); err != nil {
			t.Fatal(err)
		}
	}
	f.Invalidate(user, testCompany, testProject)
	for _, u := range []uuid.UUID{user, other} {
		if _, _, err := f.Get(ctx, u, testCompany, testProject, false); err != nil {
			t.Fatal(err)
		}
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 3 {
		t.Fatalf("calls = %d, want 3 (only the invalidated user refetches)", n)
	}
}

func TestFeatures_ProcoreDown_ErrorNotCached(t *testing.T) {
	f, fp, _ := newFeatures(t, ProbeRFIManagers)
	ctx := context.Background()
	user := uuid.New()
	fp.RFIManagersErr = fmt.Errorf("%w: boom", procore.ErrUnavailable)

	if _, _, err := f.Get(ctx, user, testCompany, testProject, false); !errors.Is(err, procore.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	fp.RFIManagersErr = &procore.APIError{Status: 500, Body: "oops"}
	var apiErr *procore.APIError
	if _, _, err := f.Get(ctx, user, testCompany, testProject, false); !errors.As(err, &apiErr) || apiErr.Status != 500 {
		t.Fatalf("err = %v, want the 500", err)
	}
	fp.RFIManagersErr = nil
	states, _, err := f.Get(ctx, user, testCompany, testProject, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := rfiState(t, states); !got.Available {
		t.Fatalf("rfi = %+v, want available once Procore answers", got)
	}
}

func TestFeatures_ProjectNotVisible_PermissionDenied(t *testing.T) {
	f, fp, _ := newFeatures(t, ProbeRFIManagers)
	fp.Err["GetProject"] = forbidden("")

	if _, _, err := f.Get(context.Background(), uuid.New(), testCompany, testProject, false); !errors.Is(err, procore.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 0 {
		t.Fatalf("probe ran %d times for an invisible project", n)
	}
}

func TestFeatures_CreateOutcomeProbe_FollowsLastOutcome(t *testing.T) {
	f, fp, clock := newFeatures(t, ProbeCreateOutcome)
	ctx := context.Background()
	user, other := uuid.New(), uuid.New()

	get := func(u uuid.UUID) FeatureState {
		t.Helper()
		states, _, err := f.Get(ctx, u, testCompany, testProject, false)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		return rfiState(t, states)
	}

	if got := get(user); !got.Available {
		t.Fatalf("no outcome: rfi = %+v, want available", got)
	}

	f.RecordCreateOutcome(user, testCompany, testProject, forbidden(`{"errors":["You do not have permission to create RFIs"]}`))
	if got := get(user); got.Available || got.Reason != "You do not have permission to create RFIs" {
		t.Fatalf("after a 403: rfi = %+v, want unavailable with Procore's reason", got)
	}
	if got := get(other); !got.Available {
		t.Fatalf("other user: rfi = %+v, want available", got)
	}

	// An error that says nothing about permission leaves the outcome alone.
	f.RecordCreateOutcome(user, testCompany, testProject, fmt.Errorf("%w: down", procore.ErrUnavailable))
	if got := get(user); got.Available {
		t.Fatalf("after a 5xx: rfi = %+v, want the 403 to stand", got)
	}

	f.RecordCreateOutcome(user, testCompany, testProject, nil)
	if got := get(user); !got.Available {
		t.Fatalf("after a success: rfi = %+v, want available", got)
	}

	f.RecordCreateOutcome(user, testCompany, testProject, &procore.APIError{Status: 404})
	if got := get(user); got.Available || got.Reason != "RFI tool not enabled on this project" {
		t.Fatalf("after a 404: rfi = %+v, want tool not enabled", got)
	}

	clock.Advance(testTTL + time.Second)
	if got := get(user); !got.Available {
		t.Fatalf("after the ttl: rfi = %+v, want available", got)
	}
	if n := fp.CallCount("ListPotentialRFIManagers"); n != 0 {
		t.Fatalf("create_outcome probe called the managers list %d times", n)
	}
}

func TestProcoreReason(t *testing.T) {
	long := ""
	for range 250 {
		long += "é"
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"errors array", forbidden(`{"errors":["App is not connected to this company."]}`), "App is not connected to this company."},
		{"errors string", forbidden(`{"errors":"forbidden"}`), "forbidden"},
		{"nested errors", forbidden(`{"errors":{"base":["You may not do that"]}}`), "You may not do that"},
		{"message", forbidden(`{"message":"Not allowed"}`), "Not allowed"},
		{"plain text", forbidden("  Access denied\n"), "Access denied"},
		{"truncated json", forbidden(`{"errors":["Cut off mid`), "Cut off mid"},
		{"cut rune", forbidden("Denied \xc3"), "Denied"},
		{"html page", forbidden("<html><body>403 Forbidden</body></html>"), "Procore permission denied"},
		{"empty body", forbidden(""), "Procore permission denied"},
		{"whitespace body", forbidden(" \n"), "Procore permission denied"},
		{"no api error", procore.ErrPermissionDenied, "Procore permission denied"},
		{"long body", forbidden(long), long[:400]},
	}
	for _, c := range cases {
		if got := ProcoreReason(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
