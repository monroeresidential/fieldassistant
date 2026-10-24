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
	CreatedAt string   `json:"created_at"`
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
	n, err := numberString(w.Number)
	if err != nil {
		return err
	}
	r.Number = n
	return nil
}

// numberString reads an RFI number that Procore sends as a JSON number, a
// string or null. A missing or null value is the empty string.
func numberString(raw json.RawMessage) (string, error) {
	n := bytes.TrimSpace(raw)
	switch {
	case len(n) == 0 || bytes.Equal(n, []byte("null")):
		return "", nil
	case n[0] == '"':
		var s string
		if err := json.Unmarshal(n, &s); err != nil {
			return "", fmt.Errorf("procore: decode rfi number: %w", err)
		}
		return s, nil
	default:
		var num json.Number
		if err := json.Unmarshal(n, &num); err != nil {
			return "", fmt.Errorf("procore: decode rfi number: %w", err)
		}
		return num.String(), nil
	}
}

// ProjectDetail is a single Procore project with its time zone.
type ProjectDetail struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	TimeZone    string `json:"time_zone"`
}

// ProjectUser is a user on a project's directory.
type ProjectUser struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	EmailAddress string `json:"email_address"`
	JobTitle     string `json:"job_title"`
	Vendor       struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"vendor"`
}

// Location is a node in a project's location tree.
type Location struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	NodeName string `json:"node_name"`
	ParentID *int64 `json:"parent_id"`
}

// SpecSection is a specification section.
type SpecSection struct {
	ID          int64  `json:"id"`
	Number      string `json:"number"`
	Description string `json:"description"`
	Label       string `json:"label"`
}

// CostCode is a project cost code.
type CostCode struct {
	ID       int64  `json:"id"`
	FullCode string `json:"full_code"`
	Name     string `json:"name"`
}

// Vendor is a company in the project's directory.
type Vendor struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	TradeName       string `json:"trade_name"`
	AbbreviatedName string `json:"abbreviated_name"`
}

// Drawing is a drawing in the project's current drawing set.
type Drawing struct {
	ID         int64  `json:"id"`
	Number     string `json:"number"`
	Title      string `json:"title"`
	Discipline string `json:"discipline"`
}

// RFIQuestion is the question part of a new RFI.
type RFIQuestion struct {
	Body      string  `json:"body"`
	UploadIDs []int64 `json:"upload_ids,omitempty"`
}

// RFIImpact is a schedule or cost impact on a new RFI.
type RFIImpact struct {
	Status string   `json:"status"`
	Value  *float64 `json:"value,omitempty"`
}

// RFICreate is the body of a new RFI. Optional fields are omitted when unset.
type RFICreate struct {
	Subject                 string      `json:"subject"`
	Question                RFIQuestion `json:"question"`
	AssigneeIDs             []int64     `json:"assignee_ids,omitempty"`
	RFIManagerID            *int64      `json:"rfi_manager_id,omitempty"`
	DistributionIDs         []int64     `json:"distribution_ids,omitempty"`
	ResponsibleContractorID *int64      `json:"responsible_contractor_id,omitempty"`
	LocationID              *int64      `json:"location_id,omitempty"`
	SpecificationSectionID  *int64      `json:"specification_section_id,omitempty"`
	DrawingNumber           *string     `json:"drawing_number,omitempty"`
	CostCodeID              *int64      `json:"cost_code_id,omitempty"`
	DueDate                 *string     `json:"due_date,omitempty"`
	ScheduleImpact          *RFIImpact  `json:"schedule_impact,omitempty"`
	CostImpact              *RFIImpact  `json:"cost_impact,omitempty"`
	Private                 *bool       `json:"private,omitempty"`
	Draft                   *bool       `json:"draft,omitempty"`
}

// RFICreated is Procore's response to creating an RFI.
type RFICreated struct {
	ID     int64  `json:"id"`
	Number string `json:"number"`
	Status string `json:"status"`
	Link   string `json:"link"`
}

// UnmarshalJSON accepts the RFI number as either a JSON number or string.
func (r *RFICreated) UnmarshalJSON(b []byte) error {
	type plain RFICreated
	var w struct {
		plain
		Number json.RawMessage `json:"number"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = RFICreated(w.plain)
	n, err := numberString(w.Number)
	if err != nil {
		return err
	}
	r.Number = n
	return nil
}
