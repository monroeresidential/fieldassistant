package report

import (
	"bytes"
	"cmp"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
)

// Corrections returns the fields, sorted, whose value in final differs from
// llmOutput: the pilot's correction count for one draft. The fields are the
// top-level keys of the two JSON objects. A field the extraction left empty
// and the user filled counts, and so does one the user emptied. Empty means
// missing, null, "", [] or {} (an object whose members are all empty, such
// as {"status":"no","value":null} against {"status":"no"}, is compared
// without those members). Nested values such as impacts are compared as
// values, so one field counts once however many times it was saved. A list
// of plain values (IDs, strings) compares as a set of values, so reordering
// it is not a correction.
//
// An empty or null final means the draft was never reviewed: its final
// payload is the extraction, so there are no corrections. A payload that is
// not a JSON object compares as an empty one.
func Corrections(llmOutput, final json.RawMessage) []string {
	if isNull(final) {
		return nil
	}
	a, b := decodeObject(llmOutput), decodeObject(final)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var fields []string
	for k := range keys {
		if !reflect.DeepEqual(normalize(a[k]), normalize(b[k])) {
			fields = append(fields, k)
		}
	}
	sort.Strings(fields)
	return fields
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func decodeObject(raw json.RawMessage) map[string]any {
	var m map[string]any
	if isNull(raw) || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

// normalize maps every empty value to nil and drops empty object members,
// so two values compare equal when they differ only in emptiness.
func normalize(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if t == "" {
			return nil
		}
		return t
	case []any:
		if len(t) == 0 {
			return nil
		}
		out := make([]any, len(t))
		scalars := true
		for i, e := range t {
			out[i] = normalize(e)
			switch out[i].(type) {
			case string, float64, bool:
			default:
				scalars = false
			}
		}
		if scalars {
			slices.SortFunc(out, func(a, b any) int { return cmp.Compare(compact(a), compact(b)) })
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range t {
			if n := normalize(e); n != nil {
				out[k] = n
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return t
	}
}
