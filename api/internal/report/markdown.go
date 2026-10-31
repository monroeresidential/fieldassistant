package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Markdown renders the summary. Every section is present, with "none" where
// it has nothing to show. Times are UTC, RFC 3339.
func Markdown(s Summary) string {
	var b strings.Builder
	p := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	p("# Pilot summary\n\n")

	p("## Window\n\n")
	p("Drafts created from %s up to, not including, %s.\n\n", stamp(s.Window.From), stamp(s.Window.To))

	p("## Drafts\n\n")
	p("| Drafts | Count |\n| --- | --- |\n")
	p("| Created | %d |\n| Built | %d |\n| Submitted | %d |\n| Failed | %d |\n\n",
		s.Drafts.Created, s.Drafts.Built, s.Drafts.Submitted, s.Drafts.Failed)
	p("Failed by error code:\n\n")
	countTable(&b, "Code", s.Drafts.FailedByCode)

	p("## Corrections\n\n")
	p("A correction is a field whose final value differs from the extraction's, including an empty one the user filled; counted per field, not per save.\n\n")
	built := len(s.Corrections.PerDraft)
	if built == 0 {
		p("Drafts over two corrections: none (no built drafts).\n\n")
	} else {
		p("Drafts over two corrections: %d of %d built (%.0f%%).\n\n",
			s.Corrections.DraftsOverTwo, built, 100*float64(s.Corrections.DraftsOverTwo)/float64(built))
	}
	if len(s.Corrections.Histogram) == 0 {
		p("Histogram: none\n\n")
	} else {
		p("| Corrections | Drafts |\n| --- | --- |\n")
		for _, n := range slices.Sorted(maps.Keys(s.Corrections.Histogram)) {
			p("| %d | %d |\n", n, s.Corrections.Histogram[n])
		}
		p("\n")
	}

	p("## Duplicates\n\n")
	p("Expected: none in each list.\n\n")
	p("### More than one submitted event\n\n")
	idList(&b, s.Duplicates.MultipleSubmitted)
	p("### Attempts with an unknown outcome at the window's end\n\n")
	idList(&b, s.Duplicates.UnknownAttempts)
	p("### Mirror RFIs matching a submitted draft's subject and creator\n\n")
	if len(s.Duplicates.MirrorMatches) == 0 {
		p("- none\n\n")
	} else {
		for _, m := range s.Duplicates.MirrorMatches {
			p("- Draft `%s` (its RFI %d): RFI %d in project %d, subject %q\n",
				m.DraftID, m.DraftRecordID, m.ProcoreID, m.ProjectID, m.Subject)
		}
		p("\n")
	}

	p("## Extraction\n\n")
	if s.Extraction.N == 0 {
		p("none\n\n")
	} else {
		p("| Extraction | Time |\n| --- | --- |\n")
		p("| p50 | %s |\n| p95 | %s |\n| n | %d |\n\n", s.Extraction.P50, s.Extraction.P95, s.Extraction.N)
	}

	p("## Photos\n\n")
	p("| Photos | Count |\n| --- | --- |\n| Pushed | %d |\n\n", s.Photos.Pushed)
	p("Failed by error code:\n\n")
	countTable(&b, "Code", s.Photos.FailedByCode)

	p("## Per user\n\n")
	if len(s.PerUser) == 0 {
		p("none\n\n")
	} else {
		p("| User | Created | Submitted | Failed | Corrections |\n| --- | --- | --- | --- | --- |\n")
		for _, r := range s.PerUser {
			p("| %s | %d | %d | %d | %d |\n", cell(r.Name), r.Created, r.Submitted, r.Failed, r.Corrections)
		}
		p("\n")
	}

	p("## Worst five\n\n")
	p("To seed an eval case from one, file it under `internal/features/rfi/evals/pilot/` once Drew confirms the expected values, in the case format of `evals/README.md`: the transcript as `transcript`, the final values of the changed fields as `expect.payload` (or `expect.absent`), and a fixture from the project's lookups.\n\n")
	if len(s.Worst) == 0 {
		p("none\n")
	}
	for i, w := range s.Worst {
		p("### %d. %s, %s, %d corrections\n\n", i+1, w.User, stamp(w.CreatedAt), len(w.Fields))
		p("Draft `%s`. Fields changed: %s.\n\n", w.ID, codeList(w.Fields))
		extracted, final := decodeObject(w.Extracted), decodeObject(w.Final)
		p("| Field | Extracted | Final |\n| --- | --- | --- |\n")
		for _, f := range w.Fields {
			p("| %s | %s | %s |\n", cell(f), cell(compact(extracted[f])), cell(compact(final[f])))
		}
		p("\nTranscript:\n\n%s\n", fenced("text", w.Transcript))
		p("Extracted:\n\n%s\n", fenced("json", indent(w.Extracted)))
		p("Final:\n\n%s", fenced("json", indent(w.Final)))
		if i < len(s.Worst)-1 {
			p("\n")
		}
	}
	return b.String()
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// countTable writes counts by code, most first, or "none".
func countTable(b *strings.Builder, label string, counts map[string]int) {
	if len(counts) == 0 {
		b.WriteString("none\n\n")
		return
	}
	codes := slices.Sorted(maps.Keys(counts))
	slices.SortStableFunc(codes, func(a, c string) int { return counts[c] - counts[a] })
	fmt.Fprintf(b, "| %s | Count |\n| --- | --- |\n", label)
	for _, c := range codes {
		fmt.Fprintf(b, "| %s | %d |\n", cell(c), counts[c])
	}
	b.WriteString("\n")
}

func idList(b *strings.Builder, ids []uuid.UUID) {
	if len(ids) == 0 {
		b.WriteString("- none\n\n")
		return
	}
	for _, id := range ids {
		fmt.Fprintf(b, "- `%s`\n", id)
	}
	b.WriteString("\n")
}

func codeList(fields []string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = "`" + f + "`"
	}
	return strings.Join(out, ", ")
}

// cell escapes a value for a table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.ReplaceAll(s, "\n", " ")
}

// compact is a payload value as one line of JSON; empty for missing.
func compact(v any) string {
	if v == nil {
		return ""
	}
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(out)
}

func indent(raw json.RawMessage) string {
	if isNull(raw) {
		return "null"
	}
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") != nil {
		return string(raw)
	}
	return buf.String()
}

// fenced wraps body in a code fence longer than any backtick run inside it.
func fenced(lang, body string) string {
	longest, run := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimRight(body, "\n") + "\n" + fence + "\n"
}
