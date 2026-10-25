package procore

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchURL_NotBoundByClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("-last"))
	}))
	defer srv.Close()
	hc := srv.Client()
	hc.Timeout = 50 * time.Millisecond
	c := NewClient("http://unused.invalid", hc, nil, nil)

	resp, err := c.FetchURL(context.Background(), srv.URL+"/file")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v (read %q)", err, b)
	}
	if string(b) != "first-last" {
		t.Errorf("body %q, want first-last", b)
	}
	if hc.Timeout != 50*time.Millisecond {
		t.Errorf("FetchURL changed the shared client's timeout to %v", hc.Timeout)
	}
}

func TestFetchURL_ContextStillBoundsTheFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	c := NewClient("http://unused.invalid", srv.Client(), nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	resp, err := c.FetchURL(ctx, srv.URL+"/file")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer resp.Body.Close()
	start := time.Now()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Error("read finished without error after the context ended")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("read took %v after the context deadline", d)
	}
}

func TestRFIDetail_DecodesLooselyTypedFields(t *testing.T) {
	body := `{
		"id": 1, "number": 7,
		"revision": 2, "drawing_number": 101, "reference": true, "full_number": null,
		"prefix": 3.5, "link": "https://app.procore.com/rfis/1", "ball_in_court_role": 4,
		"cost_impact": {"status": 1, "value": "1500.00"},
		"questions": [{"id": 10, "attachments": [
			{"id": 100, "name": "a", "size": "2048"},
			{"id": 101, "name": "b", "size": 4096},
			{"id": 102, "name": "c", "size": null},
			{"id": 103, "name": "d", "size": "unknown"}
		]}]
	}`
	var d RFIDetail
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Revision != "2" || d.DrawingNumber != "101" || d.Reference != "true" || d.FullNumber != "" || d.Prefix != "3.5" {
		t.Errorf("revision %q drawing_number %q reference %q full_number %q prefix %q", d.Revision, d.DrawingNumber, d.Reference, d.FullNumber, d.Prefix)
	}
	if d.Link != "https://app.procore.com/rfis/1" || d.BallInCourtRole != "4" {
		t.Errorf("link %q ball_in_court_role %q", d.Link, d.BallInCourtRole)
	}
	if d.CostImpact == nil || d.CostImpact.Status != "1" {
		t.Errorf("cost_impact %+v", d.CostImpact)
	}
	atts := d.Questions[0].Attachments
	want := []LooseInt64{2048, 4096, 0, 0}
	for i, a := range atts {
		if a.Size != want[i] {
			t.Errorf("attachment %d size %d, want %d", a.ID, a.Size, want[i])
		}
	}
}

func TestLooseString_RejectsObjects(t *testing.T) {
	var s LooseString
	if err := json.Unmarshal([]byte(`{"a":1}`), &s); err == nil {
		t.Error("an object decoded as a string")
	}
}
