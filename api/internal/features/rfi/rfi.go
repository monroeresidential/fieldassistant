// Package rfi is the RFI feature: its output schema, prompt and defaults.
package rfi

import (
	"embed"
	"fmt"
	"strings"
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
	"github.com/monroeresidential/fieldassistant/api/internal/pipeline"
)

//go:embed prompt.md
var promptFS embed.FS

const (
	fieldNotes = "Field notes:"
	// draftUntil is how many RFIs a user submits as Procore drafts before
	// new ones default to open.
	draftUntil = 5
)

// Feature implements pipeline.Feature for RFIs.
type Feature struct {
	prompt llm.Prompt
}

var _ pipeline.Feature = (*Feature)(nil)

// New loads the embedded prompt. It panics if the prompt is missing, which
// can only happen in a broken build.
func New() *Feature {
	p, err := llm.LoadPrompt(promptFS, "prompt.md")
	if err != nil {
		panic(err)
	}
	return &Feature{prompt: p}
}

func (*Feature) Name() string { return "rfi" }

func (*Feature) LookupKinds() []pipeline.LookupKind {
	return []pipeline.LookupKind{
		pipeline.KindUsers, pipeline.KindLocations, pipeline.KindSpecSections,
		pipeline.KindCostCodes, pipeline.KindVendors, pipeline.KindDrawings,
	}
}

func (*Feature) IDFields() []pipeline.IDField {
	return []pipeline.IDField{
		{Field: "assignee_ids", Kind: pipeline.KindUsers, Multi: true},
		{Field: "rfi_manager_id", Kind: pipeline.KindUsers},
		{Field: "distribution_ids", Kind: pipeline.KindUsers, Multi: true},
		{Field: "responsible_contractor_id", Kind: pipeline.KindVendors},
		{Field: "location_id", Kind: pipeline.KindLocations},
		{Field: "specification_section_id", Kind: pipeline.KindSpecSections},
		{Field: "cost_code_id", Kind: pipeline.KindCostCodes},
	}
}

func (*Feature) DateFields() []string { return []string{"due_date"} }

func (f *Feature) Prompt() llm.Prompt { return f.prompt }

func (*Feature) OutputSchema() map[string]any { return OutputSchema }

// UserMessage gives the model the request context, then the transcript.
func (*Feature) UserMessage(ctx pipeline.ExtractContext, transcript string) string {
	today := ctx.Today
	if loc, err := time.LoadLocation(ctx.TimeZone); err == nil {
		today = today.In(loc)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n", ctx.ProjectName)
	fmt.Fprintf(&b, "Timezone: %s\n", ctx.TimeZone)
	fmt.Fprintf(&b, "Today: %s (%s)\n", today.Format("2006-01-02"), today.Weekday())
	fmt.Fprintf(&b, "Speaker: %s (Procore ID %d)\n", ctx.UserName, ctx.UserProcoreID)
	fmt.Fprintf(&b, "\nTranscript:\n%s\n", transcript)
	return b.String()
}

// ApplyDefaults fills the fields the server owns and returns a new payload.
func (*Feature) ApplyDefaults(ctx pipeline.ExtractContext, payload map[string]any, lookups map[pipeline.LookupKind]pipeline.LookupSet) (map[string]any, []pipeline.Unresolved) {
	out := make(map[string]any, len(payload)+4)
	for k, v := range payload {
		out[k] = v
	}
	var un []pipeline.Unresolved

	if out["rfi_manager_id"] == nil {
		delete(out, "rfi_manager_id")
		if lookups[pipeline.KindUsers].Has(ctx.UserProcoreID) {
			out["rfi_manager_id"] = ctx.UserProcoreID
		} else {
			un = append(un, pipeline.Unresolved{
				Field:      "rfi_manager_id",
				Reason:     pipeline.ReasonNotFound,
				Message:    "You are not in this project's directory",
				Candidates: []pipeline.LookupRow{},
			})
		}
	}
	if out["draft"] == nil {
		out["draft"] = ctx.SubmittedCount < draftUntil
	}
	if out["private"] == nil {
		out["private"] = false
	}
	for _, k := range []string{"assignee_ids", "distribution_ids"} {
		if out[k] == nil {
			out[k] = []any{}
		}
	}

	body, _ := out["question_body"].(string)
	if !strings.Contains(body, fieldNotes) && ctx.Transcript != "" {
		notes := fieldNotes + "\n" + ctx.Transcript
		if strings.TrimSpace(body) == "" {
			out["question_body"] = notes
		} else {
			out["question_body"] = strings.TrimRight(body, "\n") + "\n\n" + notes
		}
	}
	return out, un
}
