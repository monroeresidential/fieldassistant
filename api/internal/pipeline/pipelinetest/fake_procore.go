// Package pipelinetest holds test doubles for the pipeline package. It is a
// normal package (not _test.go) so tests in other packages can import it, and
// it must not import pipeline.
package pipelinetest

import (
	"context"
	"sync"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// Call is one recorded call to the fake.
type Call struct {
	Method      string
	AccessToken string
	CompanyID   int64
	ProjectID   int64
	// RFI is the body of a CreateRFI call.
	RFI *procore.RFICreate
}

// FakeProcore answers the Procore reads and RFI writes the pipeline uses from
// scripted fields and records every call. Set the fields before use; they are
// read under the lock, so use Set* helpers or set them before concurrent calls.
type FakeProcore struct {
	Users        []procore.ProjectUser
	Locations    []procore.Location
	SpecSections []procore.SpecSection
	CostCodes    []procore.CostCode
	Vendors      []procore.Vendor
	Drawings     []procore.Drawing
	Project      procore.ProjectDetail
	RFIs         []procore.RFI
	// Created is returned by CreateRFI when no error is scripted.
	Created procore.RFICreated
	// Err maps a method name ("ListProjectUsers", "CreateRFI", ...) to the
	// error that method returns.
	Err map[string]error
	// ErrForToken scripts an error for one access token and method, and wins
	// over Err. Use it to give different users different outcomes.
	ErrForToken map[string]map[string]error
	// OnCreateRFI, when set, runs at the start of each CreateRFI call,
	// outside the lock, so a test can observe state at the moment of the POST.
	OnCreateRFI func()

	mu    sync.Mutex
	calls []Call
}

// NewFakeProcore returns an empty fake with a non-nil Err map.
func NewFakeProcore() *FakeProcore {
	return &FakeProcore{Err: map[string]error{}, ErrForToken: map[string]map[string]error{}}
}

// Calls returns a copy of the recorded calls in order.
func (f *FakeProcore) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// CallCount returns how many times method was called.
func (f *FakeProcore) CallCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Method == method {
			n++
		}
	}
	return n
}

// record logs the call and returns the scripted error for it.
func (f *FakeProcore) record(c Call) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	if err, ok := f.ErrForToken[c.AccessToken][c.Method]; ok {
		return err
	}
	return f.Err[c.Method]
}

func (f *FakeProcore) ListProjectUsers(_ context.Context, token string, companyID, projectID int64) ([]procore.ProjectUser, error) {
	if err := f.record(Call{Method: "ListProjectUsers", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.Users, nil
}

func (f *FakeProcore) ListLocations(_ context.Context, token string, companyID, projectID int64) ([]procore.Location, error) {
	if err := f.record(Call{Method: "ListLocations", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.Locations, nil
}

func (f *FakeProcore) ListSpecSections(_ context.Context, token string, companyID, projectID int64) ([]procore.SpecSection, error) {
	if err := f.record(Call{Method: "ListSpecSections", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.SpecSections, nil
}

func (f *FakeProcore) ListCostCodes(_ context.Context, token string, companyID, projectID int64) ([]procore.CostCode, error) {
	if err := f.record(Call{Method: "ListCostCodes", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.CostCodes, nil
}

func (f *FakeProcore) ListVendors(_ context.Context, token string, companyID, projectID int64) ([]procore.Vendor, error) {
	if err := f.record(Call{Method: "ListVendors", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.Vendors, nil
}

func (f *FakeProcore) ListDrawings(_ context.Context, token string, companyID, projectID int64) ([]procore.Drawing, error) {
	if err := f.record(Call{Method: "ListDrawings", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.Drawings, nil
}

func (f *FakeProcore) GetProject(_ context.Context, token string, companyID, projectID int64) (procore.ProjectDetail, error) {
	if err := f.record(Call{Method: "GetProject", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return procore.ProjectDetail{}, err
	}
	return f.Project, nil
}

func (f *FakeProcore) ListRFIs(_ context.Context, token string, companyID, projectID int64) ([]procore.RFI, error) {
	if err := f.record(Call{Method: "ListRFIs", AccessToken: token, CompanyID: companyID, ProjectID: projectID}); err != nil {
		return nil, err
	}
	return f.RFIs, nil
}

func (f *FakeProcore) CreateRFI(_ context.Context, token string, companyID, projectID int64, body procore.RFICreate) (procore.RFICreated, error) {
	if f.OnCreateRFI != nil {
		f.OnCreateRFI()
	}
	if err := f.record(Call{Method: "CreateRFI", AccessToken: token, CompanyID: companyID, ProjectID: projectID, RFI: &body}); err != nil {
		return procore.RFICreated{}, err
	}
	return f.Created, nil
}
