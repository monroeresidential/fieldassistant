package pipeline

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// NormalizeToSchema is the schema-driven part of Feature.Normalize, for
// features whose OutputSchema has a "payload" object and an "unresolved"
// array of {field, reason, ...}. It reads f.OutputSchema and f.IDFields and
// returns a new payload and unresolved list:
//
//   - A payload key the schema does not list is dropped.
//   - A value of the wrong JSON type is dropped. Integers also accept
//     integral numbers written as strings ("11"). An ID field keeps a value
//     it cannot coerce, so Validate removes it and flags not_in_lookup.
//   - A string outside its enum, or an object missing a required property
//     or holding a property outside its enum, is dropped and its field
//     flagged invalid_value. Unknown properties of an object are dropped.
//   - An unresolved reason outside the schema's reason enum becomes
//     ambiguous.
//
// null values are kept: they clear a field.
func NormalizeToSchema(f Feature, payload map[string]any, unresolved []Unresolved) (map[string]any, []Unresolved) {
	schema := f.OutputSchema()
	props := properties(schema, "payload")
	ids := map[string]bool{}
	for _, idf := range f.IDFields() {
		ids[idf.Field] = true
	}

	out := make(map[string]any, len(payload))
	var added []Unresolved
	for k, v := range payload {
		prop, ok := props[k].(map[string]any)
		if !ok {
			continue
		}
		if v == nil {
			out[k] = nil
			continue
		}
		nv, bad, ok := normalizeValue(prop, v, ids[k])
		switch {
		case bad != "":
			added = append(added, Unresolved{Field: k, Reason: ReasonInvalidValue, Message: invalidMessage(k, bad), Candidates: []LookupRow{}})
		case ok:
			out[k] = nv
		}
	}

	reasons := reasonEnum(schema)
	un := make([]Unresolved, 0, len(unresolved)+len(added))
	for _, u := range unresolved {
		if len(reasons) > 0 && !slices.Contains(reasons, u.Reason) {
			u.Reason = ReasonAmbiguous
		}
		un = append(un, u)
	}
	return out, append(un, added...)
}

// normalizeValue checks v against prop. It returns the value to keep and
// ok, or bad naming what was out of range ("" for the value itself, or the
// property of an object) when the field must be flagged. keepRaw keeps a
// value that cannot be coerced, for Validate to judge.
func normalizeValue(prop map[string]any, v any, keepRaw bool) (out any, bad string, ok bool) {
	switch prop["type"] {
	case "string":
		s, isStr := v.(string)
		if !isStr {
			return nil, "", false
		}
		if enum := stringEnum(prop); enum != nil && !slices.Contains(enum, s) {
			return nil, "value", false
		}
		return s, "", true
	case "integer":
		if n, isInt := toInteger(v); isInt {
			return n, "", true
		}
		return v, "", keepRaw
	case "number":
		switch n := v.(type) {
		case float64:
			return n, "", true
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f, "", true
			}
		}
		return nil, "", false
	case "boolean":
		b, isBool := v.(bool)
		return b, "", isBool
	case "array":
		arr, isArr := v.([]any)
		if !isArr {
			return v, "", keepRaw
		}
		items, _ := prop["items"].(map[string]any)
		kept := []any{}
		for _, item := range arr {
			if item == nil {
				continue
			}
			nv, _, ok := normalizeValue(items, item, keepRaw)
			if ok {
				kept = append(kept, nv)
			}
		}
		return kept, "", true
	case "object":
		obj, isObj := v.(map[string]any)
		if !isObj {
			return nil, "", false
		}
		sub, _ := prop["properties"].(map[string]any)
		res := map[string]any{}
		for k, sv := range obj {
			sp, known := sub[k].(map[string]any)
			if !known || sv == nil {
				continue
			}
			nv, subBad, ok := normalizeValue(sp, sv, false)
			if subBad != "" {
				return nil, k, false
			}
			if ok {
				res[k] = nv
			}
		}
		for _, req := range stringList(prop["required"]) {
			if _, have := res[req]; !have {
				return nil, req, false
			}
		}
		return res, "", true
	}
	return v, "", true
}

// toInteger accepts an integral JSON number, or such a number written as a
// string, and returns it as a float64 like the rest of a decoded payload.
func toInteger(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		if _, ok := toID(n); ok {
			return n, true
		}
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil && math.Abs(float64(i)) <= 1<<53 {
			return float64(i), true
		}
	}
	return 0, false
}

// invalidMessage is the reviewer's note for a dropped value: "invalid cost
// impact status" for an object property, "invalid cost impact" for the
// value itself.
func invalidMessage(field, prop string) string {
	name := strings.ReplaceAll(field, "_", " ")
	if prop == "value" || prop == "" {
		return "invalid " + name
	}
	return fmt.Sprintf("invalid %s %s", name, prop)
}

// ImpactValueMessage flags an impact value that is not a plain number.
const ImpactValueMessage = "value must be a plain number: dollars for cost, days for schedule"

// ParseAmount reads a plain number from a spoken or typed amount, allowing
// a leading $, thousands commas and spaces ("$2,000" is 2000).
func ParseAmount(s string) (float64, bool) {
	clean := strings.NewReplacer("$", "", ",", "", " ", "", " ", "").Replace(strings.TrimSpace(s))
	if clean == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

func properties(schema map[string]any, name string) map[string]any {
	props, _ := schema["properties"].(map[string]any)
	obj, _ := props[name].(map[string]any)
	out, _ := obj["properties"].(map[string]any)
	return out
}

func reasonEnum(schema map[string]any) []string {
	props, _ := schema["properties"].(map[string]any)
	un, _ := props["unresolved"].(map[string]any)
	items, _ := un["items"].(map[string]any)
	ip, _ := items["properties"].(map[string]any)
	reason, _ := ip["reason"].(map[string]any)
	return stringEnum(reason)
}

func stringEnum(prop map[string]any) []string {
	return stringList(prop["enum"])
}

func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, x := range l {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
