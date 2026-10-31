// Package rfi is the RFI feature: its output schema, prompt and defaults.
package rfi

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
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
	// notManagerMessage flags rfi_manager_id when the speaker is not among
	// Procore's potential RFI managers.
	notManagerMessage = "You are not an RFI manager on this project"
	// noManagersMessage flags rfi_manager_id when the potential RFI managers
	// list could not be read (a 403, the RFI tool off, a failed fetch), so
	// whether the speaker is a manager is unknown.
	noManagersMessage = "Procore did not list RFI managers for this project"
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
		pipeline.KindRFIManagers,
	}
}

// ExtractorKinds are the kinds the model may search: every lookup kind but
// rfi_managers, which only ApplyDefaults and Validate use (the prompt tells
// the model to leave rfi_manager_id to the server).
func (f *Feature) ExtractorKinds() []pipeline.LookupKind {
	var out []pipeline.LookupKind
	for _, k := range f.LookupKinds() {
		if k != pipeline.KindRFIManagers {
			out = append(out, k)
		}
	}
	return out
}

var _ pipeline.ExtractorKinder = (*Feature)(nil)

func (*Feature) IDFields() []pipeline.IDField {
	return []pipeline.IDField{
		{Field: "assignee_ids", Kind: pipeline.KindUsers, Multi: true},
		{Field: "rfi_manager_id", Kind: pipeline.KindRFIManagers},
		{Field: "distribution_ids", Kind: pipeline.KindUsers, Multi: true},
		{Field: "responsible_contractor_id", Kind: pipeline.KindVendors},
		{Field: "location_id", Kind: pipeline.KindLocations},
		{Field: "specification_section_id", Kind: pipeline.KindSpecSections},
		{Field: "cost_code_id", Kind: pipeline.KindCostCodes},
	}
}

func (*Feature) DateFields() []string { return []string{"due_date"} }

// RequiredFields are what Procore needs to create an RFI. A flag on any of
// them blocks submit whatever its reason.
func (*Feature) RequiredFields() []string {
	return []string{"subject", "question_body", "assignee_ids", "rfi_manager_id"}
}

// PayloadFields are the payload property names in OutputSchema, sorted.
func (*Feature) PayloadFields() []string {
	props := OutputSchema["properties"].(map[string]any)["payload"].(map[string]any)["properties"].(map[string]any)
	fields := make([]string, 0, len(props))
	for k := range props {
		fields = append(fields, k)
	}
	sort.Strings(fields)
	return fields
}

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

// impactFields hold {status, value} objects whose value is a plain number.
var impactFields = []string{"schedule_impact", "cost_impact"}

// Normalize fits a payload to OutputSchema (see pipeline.NormalizeToSchema),
// after rewriting impact values as plain numbers: a JSON number becomes its
// decimal string and "$2,000" becomes "2000". An impact value that is still
// not a number stays in the payload, so the reviewer sees what was said,
// and its field is flagged invalid_value.
func (f *Feature) Normalize(payload map[string]any, unresolved []pipeline.Unresolved) (map[string]any, []pipeline.Unresolved) {
	in := make(map[string]any, len(payload))
	for k, v := range payload {
		in[k] = v
	}
	for _, field := range impactFields {
		obj, ok := in[field].(map[string]any)
		if !ok {
			continue
		}
		cp := make(map[string]any, len(obj))
		for k, v := range obj {
			cp[k] = v
		}
		switch v := cp["value"].(type) {
		case float64:
			cp["value"] = strconv.FormatFloat(v, 'f', -1, 64)
		case string:
			if n, ok := pipeline.ParseAmount(v); ok {
				cp["value"] = strconv.FormatFloat(n, 'f', -1, 64)
			}
		}
		in[field] = cp
	}

	out, un := pipeline.NormalizeToSchema(f, in, unresolved)
	for _, field := range impactFields {
		obj, ok := out[field].(map[string]any)
		if !ok {
			continue
		}
		if s, _ := obj["value"].(string); strings.TrimSpace(s) != "" {
			if _, ok := pipeline.ParseAmount(s); !ok {
				un = append(un, pipeline.Unresolved{
					Field: field, Reason: pipeline.ReasonInvalidValue, Message: pipeline.ImpactValueMessage, Candidates: []pipeline.LookupRow{},
				})
			}
		}
	}
	return out, un
}

// ApplyDefaults fills the fields the server owns and returns a new payload
// and the unresolved entries it adds. A field already in unresolved is left
// for the reviewer rather than defaulted.
func (*Feature) ApplyDefaults(ctx pipeline.ExtractContext, payload map[string]any, unresolved []pipeline.Unresolved, lookups map[pipeline.LookupKind]pipeline.LookupSet) (map[string]any, []pipeline.Unresolved) {
	out := make(map[string]any, len(payload)+4)
	for k, v := range payload {
		out[k] = v
	}
	open := map[string]bool{}
	for _, u := range unresolved {
		open[u.Field] = true
	}
	var un []pipeline.Unresolved

	// The RFI manager defaults to the speaker when Procore lists them as a
	// potential RFI manager; otherwise the reviewer picks one of those. When
	// the list could not be read, the field is flagged without claiming the
	// speaker is not a manager.
	if out["rfi_manager_id"] == nil && !open["rfi_manager_id"] {
		delete(out, "rfi_manager_id")
		managers, loaded := lookups[pipeline.KindRFIManagers]
		switch {
		case !loaded || managers.Unavailable:
			un = append(un, pipeline.Unresolved{
				Field:      "rfi_manager_id",
				Reason:     pipeline.ReasonNotFound,
				Message:    noManagersMessage,
				Candidates: []pipeline.LookupRow{},
			})
		case managers.Has(ctx.UserProcoreID):
			out["rfi_manager_id"] = ctx.UserProcoreID
		default:
			un = append(un, pipeline.Unresolved{
				Field:      "rfi_manager_id",
				Reason:     pipeline.ReasonNotFound,
				Message:    notManagerMessage,
				Candidates: append([]pipeline.LookupRow{}, managers.Rows...),
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
