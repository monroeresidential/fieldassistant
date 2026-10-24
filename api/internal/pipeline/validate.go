package pipeline

import (
	"encoding/json"
	"math"
	"time"
)

const dateLayout = "2006-01-02"

// Validate enforces the rule that no ID reaches a draft unless it came from
// the lookup cache, whatever the model returned. It returns a new payload and
// unresolved list and mutates neither input.
//
//   - An ID not in lookups[kind] is removed: a single-valued field loses its
//     key, a multi-valued field keeps the remaining IDs (possibly []). Each
//     such field is flagged once as not_in_lookup.
//   - Unresolved entries for a field not in f.PayloadFields are dropped.
//     Candidates of an ID field that are not in its lookup are dropped and
//     the rest replaced with the cached rows; any other field gets no
//     candidates, since only lookup rows may be offered for picking.
//   - A date field that is not YYYY-MM-DD, or is before today in loc, is
//     removed and flagged as date_unparseable.
//   - Unresolved entries are deduplicated by field, first wins, and a
//     single-valued ID or date field that is unresolved is removed from the
//     payload.
func Validate(f Feature, payload map[string]any, unresolved []Unresolved, lookups map[LookupKind]LookupSet, today time.Time, loc *time.Location) (map[string]any, []Unresolved) {
	out := make(map[string]any, len(payload))
	for k, v := range payload {
		out[k] = v
	}
	un := make([]Unresolved, 0, len(unresolved))
	un = append(un, unresolved...)

	idFields := map[string]IDField{}
	for _, idf := range f.IDFields() {
		idFields[idf.Field] = idf
		set := lookups[idf.Kind]
		v, present := out[idf.Field]
		if !present || v == nil {
			delete(out, idf.Field)
			continue
		}
		removed := false
		if idf.Multi {
			kept := []any{}
			arr, ok := v.([]any)
			if !ok {
				removed = true
			}
			for _, item := range arr {
				if id, ok := toID(item); ok && set.Has(id) {
					kept = append(kept, item)
				} else {
					removed = true
				}
			}
			out[idf.Field] = kept
		} else if id, ok := toID(v); !ok || !set.Has(id) {
			delete(out, idf.Field)
			removed = true
		}
		if removed {
			un = append(un, Unresolved{
				Field:   idf.Field,
				Reason:  ReasonNotInLookup,
				Message: "A value was not in this project's lookup list",
			})
		}
	}

	dateFields := map[string]bool{}
	if loc == nil {
		loc = time.UTC
	}
	todayStr := today.In(loc).Format(dateLayout)
	for _, field := range f.DateFields() {
		dateFields[field] = true
		v, present := out[field]
		if !present || v == nil {
			delete(out, field)
			continue
		}
		s, ok := v.(string)
		if ok {
			if _, err := time.Parse(dateLayout, s); err != nil || s < todayStr {
				ok = false
			}
		}
		if !ok {
			delete(out, field)
			un = append(un, Unresolved{
				Field:   field,
				Reason:  ReasonDateUnparseable,
				Message: "The date was not understood or is in the past",
			})
		}
	}

	known := map[string]bool{}
	for _, field := range f.PayloadFields() {
		known[field] = true
	}
	seen := map[string]bool{}
	deduped := make([]Unresolved, 0, len(un))
	for _, u := range un {
		idf, isID := idFields[u.Field]
		if seen[u.Field] || !(known[u.Field] || isID || dateFields[u.Field]) {
			continue
		}
		seen[u.Field] = true
		cands := []LookupRow{}
		if isID {
			for _, c := range u.Candidates {
				if row, ok := lookups[idf.Kind].Find(c.ID); ok {
					cands = append(cands, row)
				}
			}
		}
		u.Candidates = cands
		if (isID && !idf.Multi) || dateFields[u.Field] {
			delete(out, u.Field)
		}
		deduped = append(deduped, u)
	}
	return out, deduped
}

// toID converts a JSON-decoded number to an ID. Non-integral or
// non-numeric values are rejected.
func toID(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n != math.Trunc(n) || math.IsInf(n, 0) || math.Abs(n) > 1<<53 {
			return 0, false
		}
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
