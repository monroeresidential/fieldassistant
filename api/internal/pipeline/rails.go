package pipeline

import (
	"log/slog"
	"time"
)

// railsZones maps the Rails time zone names Procore reports for US projects
// to IANA names.
var railsZones = map[string]string{
	"Eastern Time (US & Canada)":  "America/New_York",
	"Central Time (US & Canada)":  "America/Chicago",
	"Mountain Time (US & Canada)": "America/Denver",
	"Arizona":                     "America/Phoenix",
	"Pacific Time (US & Canada)":  "America/Los_Angeles",
	"Alaska":                      "America/Anchorage",
	"Hawaii":                      "Pacific/Honolulu",
}

// RailsZone returns the location for a Procore project time zone. A known
// Rails name is mapped to IANA, an IANA name is used as is, and anything else
// falls back to UTC with a warning.
func RailsZone(name string) *time.Location {
	if iana, ok := railsZones[name]; ok {
		name = iana
	}
	if name != "" && name != "Local" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	slog.Warn("unknown project time zone; using UTC", "time_zone", name)
	return time.UTC
}
