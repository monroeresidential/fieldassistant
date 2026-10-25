package procore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// LooseString is a string field Procore may send as a JSON string, number,
// bool or null, for display fields whose type is not pinned down by a
// captured response. A number keeps its literal text, a bool is "true" or
// "false", and null is the empty string. An object or array is an error.
type LooseString string

// UnmarshalJSON implements json.Unmarshaler.
func (s *LooseString) UnmarshalJSON(b []byte) error {
	v := bytes.TrimSpace(b)
	switch {
	case len(v) == 0 || bytes.Equal(v, []byte("null")):
		*s = ""
	case v[0] == '"':
		var str string
		if err := json.Unmarshal(v, &str); err != nil {
			return fmt.Errorf("procore: decode loose string: %w", err)
		}
		*s = LooseString(str)
	case bytes.Equal(v, []byte("true")), bytes.Equal(v, []byte("false")):
		*s = LooseString(v)
	default:
		var num json.Number
		if err := json.Unmarshal(v, &num); err != nil {
			return fmt.Errorf("procore: decode loose string: %w", err)
		}
		*s = LooseString(num.String())
	}
	return nil
}

// LooseInt64 is an integer field Procore may send as a JSON number, a
// numeric string or null, such as an attachment's size. Null, a bool and a
// string that is not a number decode as 0; a fractional number is
// truncated. An object or array is an error.
type LooseInt64 int64

// UnmarshalJSON implements json.Unmarshaler.
func (n *LooseInt64) UnmarshalJSON(b []byte) error {
	v := bytes.TrimSpace(b)
	var text string
	switch {
	case len(v) == 0 || bytes.Equal(v, []byte("null")),
		bytes.Equal(v, []byte("true")), bytes.Equal(v, []byte("false")):
		*n = 0
		return nil
	case v[0] == '"':
		if err := json.Unmarshal(v, &text); err != nil {
			return fmt.Errorf("procore: decode loose int: %w", err)
		}
		text = strings.TrimSpace(text)
	default:
		var num json.Number
		if err := json.Unmarshal(v, &num); err != nil {
			return fmt.Errorf("procore: decode loose int: %w", err)
		}
		text = num.String()
	}
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		*n = LooseInt64(i)
		return nil
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) && math.Abs(f) < math.MaxInt64 {
		*n = LooseInt64(f)
		return nil
	}
	*n = 0
	return nil
}
