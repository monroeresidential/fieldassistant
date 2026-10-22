package procore

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Me is the Procore user behind an access token.
type Me struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// Company is a Procore company the user can access.
type Company struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Project is a Procore project within a company.
type Project struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// Person is a Procore user referenced from another record, e.g. an RFI assignee.
type Person struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// RFI is a Procore request for information.
type RFI struct {
	ID        int64    `json:"id"`
	Number    string   `json:"number"`
	Subject   string   `json:"subject"`
	Status    string   `json:"status"`
	DueDate   *string  `json:"due_date"`
	Assignees []Person `json:"assignees"`
}

// UnmarshalJSON accepts Procore's RFI number as either a JSON number or string.
func (r *RFI) UnmarshalJSON(b []byte) error {
	type plain RFI
	var w struct {
		plain
		Number json.RawMessage `json:"number"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = RFI(w.plain)
	n := bytes.TrimSpace(w.Number)
	switch {
	case len(n) == 0 || bytes.Equal(n, []byte("null")):
		r.Number = ""
	case n[0] == '"':
		if err := json.Unmarshal(n, &r.Number); err != nil {
			return fmt.Errorf("procore: decode rfi number: %w", err)
		}
	default:
		var num json.Number
		if err := json.Unmarshal(n, &num); err != nil {
			return fmt.Errorf("procore: decode rfi number: %w", err)
		}
		r.Number = num.String()
	}
	return nil
}
