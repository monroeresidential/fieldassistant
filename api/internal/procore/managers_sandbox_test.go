package procore

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// TestSandbox_PotentialRFIManagers calls the live potential_rfi_managers
// endpoint on a real project and checks the response shape: a 200 with rows
// that carry id, name and login. It changes nothing in Procore. It is skipped
// unless PROCORE_SANDBOX_TOKEN (a user access token),
// PROCORE_SANDBOX_COMPANY_ID, PROCORE_SANDBOX_PROJECT_ID and PROCORE_API_URL
// are all set. Set PROCORE_SANDBOX_LOGIN to also assert that user is listed.
func TestSandbox_PotentialRFIManagers(t *testing.T) {
	token := os.Getenv("PROCORE_SANDBOX_TOKEN")
	companyEnv := os.Getenv("PROCORE_SANDBOX_COMPANY_ID")
	projectEnv := os.Getenv("PROCORE_SANDBOX_PROJECT_ID")
	apiURL := os.Getenv("PROCORE_API_URL")
	if token == "" || companyEnv == "" || projectEnv == "" || apiURL == "" {
		t.Skip("set PROCORE_SANDBOX_TOKEN, PROCORE_SANDBOX_COMPANY_ID, PROCORE_SANDBOX_PROJECT_ID and PROCORE_API_URL to run the live RFI manager check")
	}
	companyID, err := strconv.ParseInt(companyEnv, 10, 64)
	if err != nil {
		t.Fatalf("PROCORE_SANDBOX_COMPANY_ID: %v", err)
	}
	projectID, err := strconv.ParseInt(projectEnv, 10, 64)
	if err != nil {
		t.Fatalf("PROCORE_SANDBOX_PROJECT_ID: %v", err)
	}

	c := NewClient(apiURL, nil, nil, nil)
	rows, err := c.ListPotentialRFIManagers(context.Background(), token, companyID, projectID)
	if err != nil {
		t.Fatalf("ListPotentialRFIManagers: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("potential_rfi_managers returned no rows; the signed-in user should be one")
	}
	want := os.Getenv("PROCORE_SANDBOX_LOGIN")
	found := want == ""
	for _, r := range rows {
		if r.ID == 0 || r.Name == "" || r.Login == "" {
			t.Errorf("row missing id, name or login: %+v", r)
		}
		if r.Login == want {
			found = true
		}
	}
	if !found {
		t.Errorf("%s is not among the %d potential RFI managers", want, len(rows))
	}
	t.Logf("%d potential RFI managers on project %d", len(rows), projectID)
}
