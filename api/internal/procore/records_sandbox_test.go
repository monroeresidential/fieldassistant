package procore

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/rfi_show_sandbox.json is a Show RFI response captured from the
// Procore sandbox on 2026-10-25 (project 376187), with the signed-in user's
// identity redacted. It pins the shapes the reference sample leaves open:
// null number and full_number on a draft, impacts with null status and
// value, a question's created_by as a bare name, and empty answer and
// attachment lists.
func TestRFIDetail_DecodesSandboxCapture(t *testing.T) {
	b, err := os.ReadFile("testdata/rfi_show_sandbox.json")
	if err != nil {
		t.Fatal(err)
	}
	var d RFIDetail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != 182885 || d.Status != "draft" || d.TranslatedStatus != "Draft" {
		t.Errorf("id/status = %d/%q/%q", d.ID, d.Status, d.TranslatedStatus)
	}
	if !strings.HasPrefix(d.Subject, "Is additional support") {
		t.Errorf("subject = %q", d.Subject)
	}
	if d.Number != "" || d.FullNumber != "" || d.Prefix != "" {
		t.Errorf("draft numbering should decode empty: %q %q %q", d.Number, d.FullNumber, d.Prefix)
	}
	if d.CreatedBy == nil || d.CreatedBy.ID != 256705 || d.CreatedBy.Name != "Field User" {
		t.Errorf("created_by = %+v", d.CreatedBy)
	}
	if d.RFIManager == nil || d.RFIManager.ID != 99519 || d.Assignee == nil || d.Assignee.ID != 100299 {
		t.Errorf("rfi_manager/assignee = %+v / %+v", d.RFIManager, d.Assignee)
	}
	if d.CostImpact == nil || d.CostImpact.Status != "" || d.CostImpact.Value != "" {
		t.Errorf("cost_impact with null status and value = %+v", d.CostImpact)
	}
	if d.Location != nil || d.SpecificationSection != nil || d.CostCode != nil {
		t.Errorf("null lookups should be nil: %v %v %v", d.Location, d.SpecificationSection, d.CostCode)
	}
	if len(d.Questions) != 1 {
		t.Fatalf("questions = %d", len(d.Questions))
	}
	q := d.Questions[0]
	if q.ID != 182884 || q.QuestionDate != "2026-10-24T18:01:04Z" {
		t.Errorf("question id/date = %d/%q", q.ID, q.QuestionDate)
	}
	if !strings.HasPrefix(q.PlainTextBody, "Please review the twisting wood column") || q.RichTextBody == "" {
		t.Errorf("question bodies = %q / %q", q.PlainTextBody, q.RichTextBody)
	}
	if q.CreatedBy == nil || q.CreatedBy.Name != "Field User" || q.CreatedBy.ID != 0 {
		t.Errorf("question created_by = %+v, want a name with no id", q.CreatedBy)
	}
	if len(q.Attachments) != 0 || len(q.Answers) != 0 {
		t.Errorf("attachments/answers = %d/%d", len(q.Attachments), len(q.Answers))
	}
	if len(d.CustomFields) != 0 || len(d.DrawingIDs) != 0 || len(d.DistributionList) != 0 {
		t.Errorf("empty lists: %v %v %v", d.CustomFields, d.DrawingIDs, d.DistributionList)
	}
}
