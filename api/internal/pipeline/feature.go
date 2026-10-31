package pipeline

import (
	"time"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
)

// IDField names a payload field that holds Procore IDs drawn from one lookup
// kind. Multi fields hold an array of IDs; the rest hold a single ID.
type IDField struct {
	Field string
	Kind  LookupKind
	Multi bool
}

// ExtractContext is what a feature knows about the request when it builds
// the user message and applies defaults.
type ExtractContext struct {
	ProjectName    string
	TimeZone       string
	Today          time.Time
	UserName       string
	UserProcoreID  int64
	SubmittedCount int
	// Transcript is the verbatim dictation, for features that quote it in
	// the payload.
	Transcript string
}

// Feature is what a feature module supplies to the shared extraction
// pipeline. Payloads are JSON-decoded maps, so numbers arrive as float64.
//
// Call order: after extraction the pipeline calls Normalize with the model's
// payload and unresolved list, then ApplyDefaults with the normalized ones,
// appends the unresolved entries ApplyDefaults returns, and then calls
// Validate with the defaulted payload and the merged list. Validate runs last
// so that no default can reintroduce an ID or a field that validation
// removed. A reviewer's payload goes through Normalize (with no unresolved
// list) and then Validate on every save and submit.
type Feature interface {
	Name() string
	// LookupKinds are the kinds warmed for an extraction or a save: the
	// lists ApplyDefaults and Validate see. The extractor is offered a
	// list_<kind> tool for each of them unless the feature narrows that with
	// ExtractorKinds (see ToolKinds).
	LookupKinds() []LookupKind
	IDFields() []IDField
	DateFields() []string
	// PayloadFields lists every field the payload may hold. Validate drops
	// unresolved entries naming any other field.
	PayloadFields() []string
	// RequiredFields lists the fields Procore needs to create the record. An
	// unresolved entry on one of them always blocks submit (see
	// markBlocking).
	RequiredFields() []string
	Prompt() llm.Prompt
	// OutputSchema is the JSON schema of the submit_draft tool input.
	OutputSchema() map[string]any
	UserMessage(ctx ExtractContext, transcript string) string
	// ApplyDefaults returns a new payload with server-side defaults filled in
	// and the unresolved entries the defaults add. It does not default a
	// field that is already in unresolved, and must not mutate its inputs.
	ApplyDefaults(ctx ExtractContext, payload map[string]any, unresolved []Unresolved, lookups map[LookupKind]LookupSet) (map[string]any, []Unresolved)
	// Normalize makes an untrusted payload and unresolved list fit the
	// feature's schema, since neither the model's tool input nor a client's
	// edit is schema-checked: unknown or wrong-typed payload keys are
	// dropped, numeric IDs coerced, values outside an enum dropped and
	// flagged invalid_value, and reasons the model may not use mapped to
	// ambiguous. It returns the unresolved list it was given, normalized,
	// followed by the entries it adds. It must not mutate its inputs.
	Normalize(payload map[string]any, unresolved []Unresolved) (map[string]any, []Unresolved)
}

// Unresolved reasons, matching the UnresolvedField.reason enum in the contract.
const (
	ReasonAmbiguous         = "ambiguous"
	ReasonNotFound          = "not_found"
	ReasonNotInLookup       = "not_in_lookup"
	ReasonDateUnparseable   = "date_unparseable"
	ReasonProcoreValidation = "procore_validation"
	// ReasonInvalidValue flags a value of the wrong shape: an enum value
	// outside the enum, or a number field holding words.
	ReasonInvalidValue = "invalid_value"
)

// Unresolved is a field flagged for the reviewer. A blocking entry must be
// settled before submit; an advisory one is shown but does not stop it.
type Unresolved struct {
	Field      string      `json:"field"`
	Reason     string      `json:"reason"`
	Message    string      `json:"message,omitempty"`
	Candidates []LookupRow `json:"candidates"`
	// Blocking is derived by markBlocking from Reason and the feature's
	// RequiredFields; a stored value is never trusted.
	Blocking bool `json:"blocking"`
}

// markBlocking returns a copy of us (never nil) with Blocking set. An entry
// blocks when its field is in f.RequiredFields, whatever the reason, or
// when its reason means the value itself is wrong: not_in_lookup,
// invalid_value or procore_validation. not_found, ambiguous and
// date_unparseable on any other field are advisory, since the reviewer may
// have nothing to pick (a project with no locations). A nil f marks by
// reason only.
func markBlocking(f Feature, us []Unresolved) []Unresolved {
	required := map[string]bool{}
	if f != nil {
		for _, field := range f.RequiredFields() {
			required[field] = true
		}
	}
	out := make([]Unresolved, len(us))
	for i, u := range us {
		switch u.Reason {
		case ReasonNotInLookup, ReasonInvalidValue, ReasonProcoreValidation:
			u.Blocking = true
		default:
			u.Blocking = required[u.Field]
		}
		out[i] = u
	}
	return out
}

// blockingFields returns the fields of the blocking entries in us.
func blockingFields(us []Unresolved) []string {
	var fields []string
	for _, u := range us {
		if u.Blocking {
			fields = append(fields, u.Field)
		}
	}
	return fields
}

// ExtractorKinder is implemented by a Feature whose extractor is offered
// list_<kind> tools for only some of its LookupKinds; the rest are warmed
// for defaults and validation alone.
type ExtractorKinder interface {
	ExtractorKinds() []LookupKind
}

// ToolKinds returns the lookup kinds the extractor is offered as list_<kind>
// tools: the feature's ExtractorKinds when it has them, else its
// LookupKinds.
func ToolKinds(f Feature) []LookupKind {
	if ek, ok := f.(ExtractorKinder); ok {
		return ek.ExtractorKinds()
	}
	return f.LookupKinds()
}
