package httpapi

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/monroeresidential/fieldassistant/api/internal/procore"
)

// decodeRFIRecord projects a mirrored RFI body. When the full decode fails
// (detailErr is set), it projects a minimal record from the id, number,
// subject, status, created_at, updated_at and link alone, so the record
// still reaches the phone, whose cursor moves past it either way. err is
// set only when even that fails, and the caller skips the record.
func decodeRFIRecord(projectID int64, body []byte) (rec RFIRecord, detailErr, err error) {
	var d procore.RFIDetail
	detailErr = json.Unmarshal(body, &d)
	if detailErr == nil {
		return rfiRecord(projectID, d), nil, nil
	}
	var m struct {
		ID        int64               `json:"id"`
		Number    procore.LooseString `json:"number"`
		Subject   procore.LooseString `json:"subject"`
		Status    procore.LooseString `json:"status"`
		CreatedAt procore.LooseString `json:"created_at"`
		UpdatedAt procore.LooseString `json:"updated_at"`
		Link      procore.LooseString `json:"link"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return RFIRecord{}, detailErr, err
	}
	if m.ID == 0 {
		return RFIRecord{}, detailErr, errors.New("rfi body has no id")
	}
	return rfiRecord(projectID, procore.RFIDetail{
		ID: m.ID, Number: string(m.Number), Subject: string(m.Subject), Status: string(m.Status),
		CreatedAt: string(m.CreatedAt), UpdatedAt: string(m.UpdatedAt), Link: m.Link,
	}), detailErr, nil
}

// rfiRecord projects Procore's Show RFI body onto the API's RFIRecord.
// Optional refs are omitted when Procore's object is null or has id 0, empty
// optional strings are omitted, and times that do not parse are omitted (or
// zero when required). Attachment URLs become the app's proxy path, never
// Procore's expiring URL.
func rfiRecord(projectID int64, d procore.RFIDetail) RFIRecord {
	created, _ := time.Parse(time.RFC3339, d.CreatedAt)
	updated, _ := time.Parse(time.RFC3339, d.UpdatedAt)
	var due *openapi_types.Date
	if d.DueDate != nil {
		if t, err := time.Parse(procoreDateLayout, *d.DueDate); err == nil {
			due = &openapi_types.Date{Time: t}
		}
	}
	var custom *map[string]any
	if len(d.CustomFields) > 0 {
		custom = &d.CustomFields
	}
	drawings := d.DrawingIDs
	if drawings == nil {
		drawings = []int64{}
	}
	questions := make([]RFIQuestion, 0, len(d.Questions))
	for _, q := range d.Questions {
		questions = append(questions, rfiQuestion(projectID, d.ID, q))
	}
	return RFIRecord{
		Id:                    d.ID,
		Number:                d.Number,
		FullNumber:            optString(string(d.FullNumber)),
		Prefix:                optString(string(d.Prefix)),
		Subject:               d.Subject,
		Status:                d.Status,
		TranslatedStatus:      optString(d.TranslatedStatus),
		Draft:                 d.Draft,
		Private:               d.Private,
		DueDate:               due,
		InitiatedAt:           optTime(d.InitiatedAt),
		TimeResolved:          optTime(d.TimeResolved),
		CreatedAt:             created,
		UpdatedAt:             updated,
		CreatedBy:             optRef(d.CreatedBy, refName),
		Assignee:              optRef(d.Assignee, refName),
		Assignees:             refList(d.Assignees),
		BallInCourt:           optRef(d.BallInCourt, refName),
		BallInCourts:          refList(d.BallInCourts),
		BallInCourtRole:       optString(string(d.BallInCourtRole)),
		RfiManager:            optRef(d.RFIManager, refName),
		ReceivedFrom:          optRef(d.ReceivedFrom, refName),
		ResponsibleContractor: optRef(d.ResponsibleContractor, refName),
		DistributionList:      refList(d.DistributionList),
		Location:              optRef(d.Location, refName),
		SpecificationSection:  optRef(d.SpecificationSection, specSectionName),
		CostCode:              optRef(d.CostCode, costCodeName),
		CostImpact:            impactRef(d.CostImpact),
		ScheduleImpact:        impactRef(d.ScheduleImpact),
		DrawingNumber:         optString(string(d.DrawingNumber)),
		DrawingIds:            drawings,
		Reference:             optString(string(d.Reference)),
		Revision:              optString(string(d.Revision)),
		CurrentRevision:       d.CurrentRevision,
		HasRevisions:          d.HasRevisions,
		Questions:             questions,
		CustomFields:          custom,
		Link:                  optString(string(d.Link)),
	}
}

func rfiQuestion(projectID, rfiID int64, q procore.RFIQuestionDetail) RFIQuestion {
	answers := make([]RFIAnswer, 0, len(q.Answers))
	for _, a := range q.Answers {
		answers = append(answers, RFIAnswer{
			Id:            a.ID,
			AnswerDate:    optTime(&a.AnswerDate),
			PlainTextBody: a.PlainTextBody,
			RichTextBody:  optString(a.RichTextBody),
			CreatedBy:     optRef(a.CreatedBy, refName),
			Attachments:   rfiAttachments(projectID, rfiID, a.Attachments),
		})
	}
	return RFIQuestion{
		Id:            q.ID,
		QuestionDate:  optTime(&q.QuestionDate),
		PlainTextBody: q.PlainTextBody,
		RichTextBody:  optString(q.RichTextBody),
		CreatedBy:     optRef(q.CreatedBy, refName),
		Attachments:   rfiAttachments(projectID, rfiID, q.Attachments),
		Answers:       answers,
	}
}

// rfiAttachments maps attachments to the proxy path
// /v1/projects/{projectID}/records/rfi/{rfiID}/attachments/{id}.
func rfiAttachments(projectID, rfiID int64, atts []procore.RFIAttachmentDetail) []RFIAttachment {
	out := make([]RFIAttachment, 0, len(atts))
	prefix := "/v1/projects/" + strconv.FormatInt(projectID, 10) + "/records/rfi/" + strconv.FormatInt(rfiID, 10) + "/attachments/"
	for _, a := range atts {
		out = append(out, RFIAttachment{
			Id:       a.ID,
			Name:     a.Name,
			Filename: optString(a.Filename),
			Url:      prefix + strconv.FormatInt(a.ID, 10),
		})
	}
	return out
}

// refName names a person, company or location.
func refName(r procore.Ref) string { return r.Name }

// specSectionName is Procore's label, else "<number> <description>".
func specSectionName(r procore.Ref) string {
	if r.Label != "" {
		return r.Label
	}
	return strings.TrimSpace(r.Number + " " + r.Description)
}

// costCodeName is "<full_code> <name>".
func costCodeName(r procore.Ref) string {
	return strings.TrimSpace(r.FullCode + " " + r.Name)
}

// optRef is nil for a null ref or one with id 0.
func optRef(r *procore.Ref, name func(procore.Ref) string) *Ref {
	if r == nil || r.ID == 0 {
		return nil
	}
	return &Ref{Id: r.ID, Name: name(*r)}
}

// refList maps people refs, dropping any with id 0; it is never nil.
func refList(rs []procore.Ref) []Ref {
	out := make([]Ref, 0, len(rs))
	for _, r := range rs {
		if r.ID != 0 {
			out = append(out, Ref{Id: r.ID, Name: r.Name})
		}
	}
	return out
}

// impactRef is nil for a null impact or one with neither status nor value.
func impactRef(i *procore.Impact) *ImpactRef {
	if i == nil || (i.Status == "" && i.Value == "") {
		return nil
	}
	return &ImpactRef{Status: optString(i.Status), Value: optString(i.Value)}
}

func optString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// optTime parses an RFC 3339 time, nil when absent or unparsable.
func optTime(s *string) *time.Time {
	if s == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		return nil
	}
	return &t
}
