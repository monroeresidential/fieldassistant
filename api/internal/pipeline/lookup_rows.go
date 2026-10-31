package pipeline

import "github.com/monroeresidential/fieldassistant/api/internal/procore"

// rowsFor maps the typed Procore slice for kind to lookup rows. It returns a
// non-nil slice, empty for an unknown kind or a mismatched items type.
func rowsFor(kind LookupKind, items any) []LookupRow {
	rows := []LookupRow{}
	switch list := items.(type) {
	case []procore.ProjectUser:
		if kind != KindUsers {
			return rows
		}
		for _, u := range list {
			rows = append(rows, LookupRow{ID: u.ID, Label: u.Name, Extra: extra(
				"role", u.JobTitle, "company", u.Vendor.Name, "email", u.EmailAddress)})
		}
	case []procore.Location:
		if kind != KindLocations {
			return rows
		}
		for _, x := range list {
			rows = append(rows, LookupRow{ID: x.ID, Label: x.Name, Extra: extra("node", x.NodeName)})
		}
	case []procore.SpecSection:
		if kind != KindSpecSections {
			return rows
		}
		for _, x := range list {
			rows = append(rows, LookupRow{ID: x.ID, Label: x.Number + " " + x.Description, Extra: extra("number", x.Number)})
		}
	case []procore.CostCode:
		if kind != KindCostCodes {
			return rows
		}
		for _, x := range list {
			rows = append(rows, LookupRow{ID: x.ID, Label: x.FullCode + " " + x.Name, Extra: extra("code", x.FullCode)})
		}
	case []procore.Vendor:
		if kind != KindVendors {
			return rows
		}
		for _, x := range list {
			rows = append(rows, LookupRow{ID: x.ID, Label: x.Name, Extra: extra("trade", x.TradeName, "abbr", x.AbbreviatedName)})
		}
	case []procore.Drawing:
		if kind != KindDrawings {
			return rows
		}
		for _, x := range list {
			rows = append(rows, LookupRow{ID: x.ID, Label: x.Number + " " + x.Title, Extra: extra("number", x.Number, "discipline", x.Discipline)})
		}
	case []procore.RFIManager:
		if kind != KindRFIManagers {
			return rows
		}
		for _, x := range list {
			rows = append(rows, LookupRow{ID: x.ID, Label: x.Name, Extra: extra("login", x.Login)})
		}
	}
	return rows
}

// extra builds an Extra map from key/value pairs, skipping empty values. It
// returns nil when nothing is left so the field is omitted from JSON.
func extra(kv ...string) map[string]string {
	var m map[string]string
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] == "" {
			continue
		}
		if m == nil {
			m = map[string]string{}
		}
		m[kv[i]] = kv[i+1]
	}
	return m
}
