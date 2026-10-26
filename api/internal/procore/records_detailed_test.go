package procore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestListRFIsDetailed_ReplacesListBodiesWithShow(t *testing.T) {
	var inFlight, peak atomic.Int32
	handlers := map[string]http.HandlerFunc{
		rfisPath: func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, rawRFIs(1, 10)) },
	}
	for i := 1; i <= 10; i++ {
		id := i
		handlers[fmt.Sprintf("%s/%d", rfisPath, id)] = func(w http.ResponseWriter, r *http.Request) {
			n := inFlight.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			inFlight.Add(-1)
			writeJSON(w, 200, fmt.Sprintf(`{"id":%d,"subject":"S%d","updated_at":"2026-10-21T13:00:00Z","questions":[{"id":%d,"plain_text_body":"Q%d","attachments":[],"answers":[]}]}`, id, id, id*10, id))
		}
	}
	f := newFakeProcore(t, handlers)
	c := NewClient(f.srv.URL, f.srv.Client(), nil, nil)
	since := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	got, err := c.ListRFIsDetailed(context.Background(), "tok", 4292442, 376187, &since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Fatalf("got %d records, want 10", len(got))
	}
	for i, r := range got {
		if r.ID != int64(i+1) {
			t.Fatalf("record %d has id %d: order must follow the list", i, r.ID)
		}
		var body struct {
			Questions []struct {
				PlainTextBody string `json:"plain_text_body"`
			} `json:"questions"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Questions) != 1 || body.Questions[0].PlainTextBody != fmt.Sprintf("Q%d", r.ID) {
			t.Errorf("record %d body = %s, want the Show body", r.ID, r.Body)
		}
		// The list's updated_at is the delta high-water mark, not the Show body's.
		if want := time.Date(2026, 10, 21, 12, 0, i%60, 0, time.UTC); !r.UpdatedAt.Equal(want) {
			t.Errorf("record %d UpdatedAt = %v, want the list's %v", r.ID, r.UpdatedAt, want)
		}
	}
	if p := peak.Load(); p > showConcurrency || p < 2 {
		t.Errorf("peak concurrent Show requests = %d, want 2..%d", p, showConcurrency)
	}
	reqs := f.recorded()
	if len(reqs) != 11 {
		t.Errorf("requests = %d, want 1 list + 10 show", len(reqs))
	}
	if q := reqs[0].Query["filters[updated_at]"]; !strings.HasPrefix(q, "2026-10-20T10:00:00Z...") {
		t.Errorf("list filters[updated_at] = %q", q)
	}
}

func TestListRFIsDetailed_DropsRFIsGoneBeforeShow(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		rfisPath:        func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, rawRFIs(1, 3)) },
		rfisPath + "/1": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{"id":1,"questions":[]}`) },
		rfisPath + "/2": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 404, `{"errors":"not found"}`) },
		rfisPath + "/3": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{"id":3,"questions":[]}`) },
	})
	c := NewClient(f.srv.URL, f.srv.Client(), nil, nil)
	got, err := c.ListRFIsDetailed(context.Background(), "tok", 4292442, 376187, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("got ids %v, want [1 3]", recordIDs(got))
	}
}

func TestListRFIsDetailed_ShowFailureFailsThePull(t *testing.T) {
	f := newFakeProcore(t, map[string]http.HandlerFunc{
		rfisPath:        func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, rawRFIs(1, 2)) },
		rfisPath + "/1": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, `{"id":1}`) },
		rfisPath + "/2": func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 403, `{"errors":"forbidden"}`) },
	})
	c := NewClient(f.srv.URL, f.srv.Client(), nil, nil)
	_, err := c.ListRFIsDetailed(context.Background(), "tok", 4292442, 376187, nil)
	if !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
}

func recordIDs(recs []RawRecord) []int64 {
	out := make([]int64, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}
