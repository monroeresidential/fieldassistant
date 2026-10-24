package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
)

func TestScripted_ToolThenFinal(t *testing.T) {
	s := &Scripted{
		Model: "claude-scripted",
		Steps: []Step{
			{Tool: "list_users", Input: `{"query":"ben"}`},
			{Tool: "list_locations", Input: `{}`},
			{Final: `{"subject":"x"}`},
		},
	}
	var names []string
	handle := func(_ context.Context, call llm.ToolCall) (json.RawMessage, error) {
		names = append(names, call.Name)
		if call.Name == "list_locations" {
			return nil, errors.New("unavailable")
		}
		return json.RawMessage(`[{"id":1}]`), nil
	}
	req := llm.Request{User: "hi", FinalTool: "submit_draft", Tools: []llm.Tool{{Name: "list_users"}}}
	res, err := s.Extract(context.Background(), req, handle)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(res.Final) != `{"subject":"x"}` || res.Model != "claude-scripted" || res.ToolCalls != 2 {
		t.Errorf("result = %+v", res)
	}
	if len(names) != 2 || names[0] != "list_users" || names[1] != "list_locations" {
		t.Errorf("handler calls = %v", names)
	}
	if len(s.Results) != 2 || string(s.Results[0]) != `[{"id":1}]` || string(s.Results[1]) != `{"error":"unavailable"}` {
		t.Errorf("Results = %s", s.Results)
	}
	if len(s.Requests) != 1 || s.Requests[0].Tools[0].Name != "list_users" {
		t.Errorf("Requests = %+v", s.Requests)
	}
	if len(res.Turns) != 6 {
		t.Errorf("Turns = %d, want 6", len(res.Turns))
	}
}

func TestScripted_ErrStepAndRunningOut(t *testing.T) {
	boom := errors.New("boom")
	handle := func(context.Context, llm.ToolCall) (json.RawMessage, error) { return nil, nil }
	if _, err := (&Scripted{Steps: []Step{{Err: boom}}}).Extract(context.Background(), llm.Request{}, handle); !errors.Is(err, boom) {
		t.Errorf("Err step: err = %v, want boom", err)
	}
	if _, err := (&Scripted{}).Extract(context.Background(), llm.Request{}, handle); !errors.Is(err, llm.ErrNoFinal) {
		t.Errorf("no steps: err = %v, want ErrNoFinal", err)
	}
}
