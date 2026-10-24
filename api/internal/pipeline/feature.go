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
type Feature interface {
	Name() string
	LookupKinds() []LookupKind
	IDFields() []IDField
	DateFields() []string
	Prompt() llm.Prompt
	// OutputSchema is the JSON schema of the submit_draft tool input.
	OutputSchema() map[string]any
	UserMessage(ctx ExtractContext, transcript string) string
	// ApplyDefaults returns a new payload with server-side defaults filled in
	// and any unresolved fields the defaults could not settle. It must not
	// mutate payload.
	ApplyDefaults(ctx ExtractContext, payload map[string]any, lookups map[LookupKind]LookupSet) (map[string]any, []Unresolved)
}

// Unresolved reasons, matching the UnresolvedField.reason enum in the contract.
const (
	ReasonAmbiguous         = "ambiguous"
	ReasonNotFound          = "not_found"
	ReasonNotInLookup       = "not_in_lookup"
	ReasonDateUnparseable   = "date_unparseable"
	ReasonProcoreValidation = "procore_validation"
)

// Unresolved is a field the user must settle during review.
type Unresolved struct {
	Field      string      `json:"field"`
	Reason     string      `json:"reason"`
	Message    string      `json:"message,omitempty"`
	Candidates []LookupRow `json:"candidates"`
}
