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
	// CreatedBy is the Procore user who created the RFI.
	CreatedBy Person `json:"created_by"`
	// Link is the RFI's web URL in Procore.
	Link string `json:"link"`
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

// Ref is a person or lookup record embedded in an RFI. Procore sends a
// different subset of Label, Number, Description and FullCode per kind
// (a specification section has label and number, a cost code full_code).
type Ref struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Label       string `json:"label"`
	Number      string `json:"number"`
	Description string `json:"description"`
	FullCode    string `json:"full_code"`
}

// UnmarshalJSON accepts Number as a JSON number, string or null, and the
// whole value as a bare JSON string, which becomes Name with no ID: the
// Show RFI response gives a question's created_by that way (Procore's
// reference sample and the sandbox, 2026-10-25).
func (r *Ref) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var name string
		if err := json.Unmarshal(b, &name); err != nil {
			return err
		}
		*r = Ref{Name: name}
		return nil
	}
	type plain Ref
	var w struct {
		plain
		Number json.RawMessage `json:"number"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = Ref(w.plain)
	n, err := numberString(w.Number)
	if err != nil {
		return err
	}
	r.Number = n
	return nil
}

// Impact is an RFI's cost or schedule impact as Procore returns it.
type Impact struct {
	Status string `json:"status"`
	Value  string `json:"value"`
}

// UnmarshalJSON accepts Value as a JSON number, string or null.
func (i *Impact) UnmarshalJSON(b []byte) error {
	var w struct {
		Status LooseString     `json:"status"`
		Value  json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	v, err := numberString(w.Value)
	if err != nil {
		return err
	}
	*i = Impact{Status: string(w.Status), Value: v}
	return nil
}

// RFIAttachmentDetail is a file attached to an RFI question or answer. URL is
// Procore's expiring download link.
type RFIAttachmentDetail struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Filename    string     `json:"filename"`
	URL         string     `json:"url"`
	ContentType string     `json:"content_type"`
	Size        LooseInt64 `json:"size"`
}

// RFIAnswerDetail is a response to an RFI question.
type RFIAnswerDetail struct {
	ID            int64                 `json:"id"`
	AnswerDate    string                `json:"answer_date"`
	PlainTextBody string                `json:"plain_text_body"`
	RichTextBody  string                `json:"rich_text_body"`
	CreatedBy     *Ref                  `json:"created_by"`
	Attachments   []RFIAttachmentDetail `json:"attachments"`
}

// RFIQuestionDetail is a question on an RFI with its attachments and answers.
type RFIQuestionDetail struct {
	ID            int64                 `json:"id"`
	QuestionDate  string                `json:"question_date"`
	PlainTextBody string                `json:"plain_text_body"`
	RichTextBody  string                `json:"rich_text_body"`
	CreatedBy     *Ref                  `json:"created_by"`
	Attachments   []RFIAttachmentDetail `json:"attachments"`
	Answers       []RFIAnswerDetail     `json:"answers"`
}

// RFIDetail is Procore's Show RFI response. Every field tolerates null or
// absence. Display strings whose JSON type is not pinned down by a captured
// response are LooseString, so a number where a string was expected does
// not fail the whole record.
type RFIDetail struct {
	ID                    int64               `json:"id"`
	Number                string              `json:"number"`
	FullNumber            LooseString         `json:"full_number"`
	Prefix                LooseString         `json:"prefix"`
	Subject               string              `json:"subject"`
	Status                string              `json:"status"`
	TranslatedStatus      string              `json:"translated_status"`
	Draft                 bool                `json:"draft"`
	Private               bool                `json:"private"`
	DueDate               *string             `json:"due_date"`
	InitiatedAt           *string             `json:"initiated_at"`
	TimeResolved          *string             `json:"time_resolved"`
	CreatedAt             string              `json:"created_at"`
	UpdatedAt             string              `json:"updated_at"`
	CreatedBy             *Ref                `json:"created_by"`
	Assignee              *Ref                `json:"assignee"`
	Assignees             []Ref               `json:"assignees"`
	BallInCourt           *Ref                `json:"ball_in_court"`
	BallInCourts          []Ref               `json:"ball_in_courts"`
	BallInCourtRole       LooseString         `json:"ball_in_court_role"`
	RFIManager            *Ref                `json:"rfi_manager"`
	ReceivedFrom          *Ref                `json:"received_from"`
	ResponsibleContractor *Ref                `json:"responsible_contractor"`
	DistributionList      []Ref               `json:"distribution_list"`
	Location              *Ref                `json:"location"`
	SpecificationSection  *Ref                `json:"specification_section"`
	CostCode              *Ref                `json:"cost_code"`
	CostImpact            *Impact             `json:"cost_impact"`
	ScheduleImpact        *Impact             `json:"schedule_impact"`
	DrawingNumber         LooseString         `json:"drawing_number"`
	DrawingIDs            []int64             `json:"drawing_ids"`
	Reference             LooseString         `json:"reference"`
	Revision              LooseString         `json:"revision"`
	CurrentRevision       bool                `json:"current_revision"`
	HasRevisions          bool                `json:"has_revisions"`
	Questions             []RFIQuestionDetail `json:"questions"`
	CustomFields          map[string]any      `json:"custom_fields"`
	Link                  LooseString         `json:"link"`
}

// UnmarshalJSON accepts the RFI number as either a JSON number or string.
func (r *RFIDetail) UnmarshalJSON(b []byte) error {
	type plain RFIDetail
	var w struct {
		plain
		Number json.RawMessage `json:"number"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*r = RFIDetail(w.plain)
	n, err := numberString(w.Number)
	if err != nil {
		return err
	}
	r.Number = n
	return nil
}
