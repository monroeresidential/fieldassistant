package rfi

// OutputSchema is the JSON schema of the submit_draft tool input. payload
// mirrors the contract's RFIDraftPayload with additionalProperties false so
// the model cannot invent fields. The model's candidates carry id and label
// only; pipeline.Validate replaces them with full rows from the lookup cache.
var OutputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"payload", "unresolved", "confidence"},
	"properties": map[string]any{
		"payload": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"subject":                   str("The question, at most 100 characters."),
				"question_body":             str("Question, context, what is blocked, then a blank line, 'Field notes:' and the verbatim transcript."),
				"assignee_ids":              ids("User ids from list_users."),
				"rfi_manager_id":            id("Leave empty; the server sets it."),
				"distribution_ids":          ids("User ids from list_users."),
				"responsible_contractor_id": id("Vendor id from list_vendors."),
				"location_id":               id("Location id from list_locations."),
				"specification_section_id":  id("Spec section id from list_spec_sections."),
				"drawing_number":            str("Sheet number as spoken, e.g. M-301."),
				"cost_code_id":              id("Cost code id from list_cost_codes. Only when spoken."),
				"due_date":                  map[string]any{"type": "string", "format": "date", "description": "YYYY-MM-DD. Only when spoken."},
				"schedule_impact":           impact,
				"cost_impact":               impact,
				"private":                   map[string]any{"type": "boolean", "description": "Only when spoken."},
				"draft":                     map[string]any{"type": "boolean", "description": "Leave empty; the server sets it."},
			},
		},
		"unresolved": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"field", "reason", "candidates"},
				"properties": map[string]any{
					"field":   str("The payload field name."),
					"reason":  map[string]any{"type": "string", "enum": []string{"ambiguous", "not_found"}},
					"message": str("Short note for the reviewer."),
					"candidates": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"id", "label"},
							"properties": map[string]any{
								"id":    map[string]any{"type": "integer"},
								"label": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
		"confidence": map[string]any{
			"type":                 "object",
			"description":          "Per payload field, 0 to 1.",
			"additionalProperties": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		},
	},
}

var impact = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"status"},
	"properties": map[string]any{
		"status": map[string]any{"type": "string", "enum": []string{"yes_known", "yes_unknown", "no", "tbd"}},
		"value":  str("Amount or duration as spoken, for yes_known."),
	},
}

func str(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func id(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func ids(desc string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": desc}
}
