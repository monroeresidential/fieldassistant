package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAnthropic speaks the Messages API: it returns scripted response
// bodies in order and records every request body it receives.
type fakeAnthropic struct {
	srv       *httptest.Server
	mu        sync.Mutex
	responses []string
	bodies    []map[string]any
	delay     time.Duration
}

func newFakeAnthropic(t *testing.T, responses ...string) *fakeAnthropic {
	t.Helper()
	f := &fakeAnthropic{responses: responses}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		f.mu.Lock()
		n := len(f.bodies)
		f.bodies = append(f.bodies, body)
		delay := f.delay
		f.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if n >= len(f.responses) {
			t.Errorf("unexpected request #%d", n+1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"no more responses"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, f.responses[n])
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAnthropic) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

func toolUseResponse(id, name, input string) string {
	return `{"id":"msg_` + id + `","type":"message","role":"assistant","model":"claude-test-reported",` +
		`"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`
}

func endTurnResponse(text string) string {
	return `{"id":"msg_end","type":"message","role":"assistant","model":"claude-test-reported",` +
		`"content":[{"type":"text","text":"` + text + `"}],` +
		`"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`
}

func testRequest() Request {
	return Request{
		System: "You extract RFIs.",
		User:   "Ask Ben about the beam.",
		Tools: []Tool{
			{Name: "list_users", Description: "List users.", InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []string{"query"},
			}},
			{Name: "submit_draft", Description: "Submit the draft.", InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"subject": map[string]any{"type": "string"}},
			}},
		},
		FinalTool:    "submit_draft",
		MaxToolCalls: 5,
	}
}

func newTestClient(f *fakeAnthropic, timeout time.Duration) *Client {
	c := NewClient("test-key", "claude-test", f.srv.URL, timeout)
	c.maxRetries = 0
	return c
}

// lastMessageBlocks returns the content blocks of the last message in a
// request body, which must be a user message.
func lastMessageBlocks(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("request has no messages: %v", body)
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("last message role = %v, want user", last["role"])
	}
	content, _ := last["content"].([]any)
	var out []map[string]any
	for _, c := range content {
		m, _ := c.(map[string]any)
		out = append(out, m)
	}
	return out
}

func jsonEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %q", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("want is not JSON: %q", want)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("JSON = %s, want %s", gb, wb)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func TestClient_Extract_ToolLoopThenFinal(t *testing.T) {
	f := newFakeAnthropic(t,
		toolUseResponse("toolu_1", "list_users", `{"query":"ben"}`),
		toolUseResponse("toolu_2", "submit_draft", `{"subject":"x"}`),
	)
	var calls []ToolCall
	handle := func(_ context.Context, call ToolCall) (json.RawMessage, error) {
		calls = append(calls, call)
		return json.RawMessage(`[{"id":42,"name":"Ben Ortiz"}]`), nil
	}
	res, err := newTestClient(f, 10*time.Second).Extract(context.Background(), testRequest(), handle)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(calls) != 1 || calls[0].Name != "list_users" || calls[0].ID != "toolu_1" {
		t.Fatalf("handler calls = %+v, want one list_users call", calls)
	}
	jsonEqual(t, calls[0].Input, `{"query":"ben"}`)

	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	first := reqs[0]
	if first["model"] != "claude-test" {
		t.Errorf("model = %v, want claude-test", first["model"])
	}
	if first["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want 4096", first["max_tokens"])
	}
	if tc, _ := first["tool_choice"].(map[string]any); tc["type"] != "any" {
		t.Errorf("tool_choice = %v, want any", first["tool_choice"])
	}
	tools, _ := first["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want both tools", first["tools"])
	}
	wantTool := `{"name":"list_users","description":"List users.","input_schema":` +
		`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}`
	jsonEqual(t, json.RawMessage(mustJSON(t, tools[0])), wantTool)
	if !strings.Contains(mustJSON(t, first["system"]), "You extract RFIs.") {
		t.Errorf("system = %v, want the system prompt", first["system"])
	}
	blocks := lastMessageBlocks(t, reqs[1])
	if len(blocks) != 1 || blocks[0]["type"] != "tool_result" || blocks[0]["tool_use_id"] != "toolu_1" {
		t.Fatalf("second request last message = %v, want one tool_result for toolu_1", blocks)
	}
	if !strings.Contains(mustJSON(t, blocks[0]["content"]), "Ben Ortiz") {
		t.Errorf("tool_result content = %v, want the handler JSON", blocks[0]["content"])
	}
	if isErr, _ := blocks[0]["is_error"].(bool); isErr {
		t.Errorf("tool_result is_error = true, want false")
	}

	jsonEqual(t, res.Final, `{"subject":"x"}`)
	if res.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", res.ToolCalls)
	}
	if len(res.Turns) != 4 {
		t.Fatalf("Turns = %d, want 4", len(res.Turns))
	}
	wantRoles := []string{"user", "assistant", "user", "assistant"}
	for i, turn := range res.Turns {
		if turn.Role != wantRoles[i] {
			t.Errorf("Turns[%d].Role = %q, want %q", i, turn.Role, wantRoles[i])
		}
		if !json.Valid(turn.Content) {
			t.Errorf("Turns[%d].Content is not JSON: %q", i, turn.Content)
		}
	}
	if !strings.Contains(string(res.Turns[0].Content), "Ask Ben about the beam.") {
		t.Errorf("Turns[0] = %s, want the user text", res.Turns[0].Content)
	}
	if !strings.Contains(string(res.Turns[2].Content), "Ben Ortiz") {
		t.Errorf("Turns[2] = %s, want the tool_result", res.Turns[2].Content)
	}
	if !strings.Contains(string(res.Turns[3].Content), "submit_draft") {
		t.Errorf("Turns[3] = %s, want the final tool_use", res.Turns[3].Content)
	}
	if res.Model != "claude-test-reported" {
		t.Errorf("Model = %q, want the model the API reported", res.Model)
	}
	if res.InputTokens != 20 || res.OutputTokens != 10 {
		t.Errorf("tokens = %d/%d, want 20/10", res.InputTokens, res.OutputTokens)
	}
	if res.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", res.Duration)
	}
}

func TestClient_Extract_ToolBudget_ForcesFinal(t *testing.T) {
	f := newFakeAnthropic(t,
		toolUseResponse("toolu_1", "list_users", `{"query":"ben"}`),
		toolUseResponse("toolu_2", "list_users", `{"query":"benjamin"}`),
		toolUseResponse("toolu_3", "submit_draft", `{"subject":"x"}`),
	)
	handled := 0
	handle := func(context.Context, ToolCall) (json.RawMessage, error) {
		handled++
		return json.RawMessage(`[]`), nil
	}
	req := testRequest()
	req.MaxToolCalls = 1
	res, err := newTestClient(f, 10*time.Second).Extract(context.Background(), req, handle)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if handled != 1 {
		t.Errorf("handler called %d times, want 1", handled)
	}
	if res.ToolCalls != 1 {
		t.Errorf("ToolCalls = %d, want 1", res.ToolCalls)
	}
	jsonEqual(t, res.Final, `{"subject":"x"}`)

	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	third := reqs[2]
	tc, _ := third["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "submit_draft" {
		t.Errorf("third tool_choice = %v, want tool submit_draft", third["tool_choice"])
	}
	tools, _ := third["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("third request tools = %v, want only submit_draft", tools)
	}
	if tool, _ := tools[0].(map[string]any); tool["name"] != "submit_draft" {
		t.Errorf("third request tool = %v, want submit_draft", tools[0])
	}
	// The over-budget tool_use still gets a tool_result so the conversation stays valid.
	blocks := lastMessageBlocks(t, third)
	if len(blocks) != 1 || blocks[0]["type"] != "tool_result" || blocks[0]["tool_use_id"] != "toolu_2" {
		t.Fatalf("third request last message = %v, want a tool_result for toolu_2", blocks)
	}
	if isErr, _ := blocks[0]["is_error"].(bool); !isErr {
		t.Errorf("over-budget tool_result is_error = false, want true")
	}
}

func TestClient_Extract_EndTurnWithoutFinal_ErrNoFinal(t *testing.T) {
	f := newFakeAnthropic(t, endTurnResponse("I am not sure."))
	handle := func(context.Context, ToolCall) (json.RawMessage, error) {
		t.Error("handler should not be called")
		return nil, nil
	}
	res, err := newTestClient(f, 10*time.Second).Extract(context.Background(), testRequest(), handle)
	if !errors.Is(err, ErrNoFinal) {
		t.Fatalf("err = %v, want ErrNoFinal", err)
	}
	if len(res.Turns) != 2 {
		t.Errorf("Turns = %d, want 2 (kept for the audit row)", len(res.Turns))
	}
}

func TestClient_Extract_HandlerError_IsToolResultError(t *testing.T) {
	f := newFakeAnthropic(t,
		toolUseResponse("toolu_1", "list_users", `{"query":"ben"}`),
		toolUseResponse("toolu_2", "submit_draft", `{"subject":"x"}`),
	)
	handle := func(context.Context, ToolCall) (json.RawMessage, error) {
		return nil, errors.New("lookup cache unavailable")
	}
	res, err := newTestClient(f, 10*time.Second).Extract(context.Background(), testRequest(), handle)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	jsonEqual(t, res.Final, `{"subject":"x"}`)
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	blocks := lastMessageBlocks(t, reqs[1])
	if len(blocks) != 1 || blocks[0]["type"] != "tool_result" {
		t.Fatalf("second request last message = %v, want one tool_result", blocks)
	}
	if isErr, _ := blocks[0]["is_error"].(bool); !isErr {
		t.Errorf("tool_result is_error = false, want true")
	}
	if !strings.Contains(mustJSON(t, blocks[0]["content"]), "lookup cache unavailable") {
		t.Errorf("tool_result content = %v, want the error text", blocks[0]["content"])
	}
}

func TestClient_Extract_ParallelOverBudgetCalls_ForceFinal(t *testing.T) {
	parallel := `{"id":"msg_p","type":"message","role":"assistant","model":"claude-test-reported","content":[` +
		`{"type":"tool_use","id":"toolu_a","name":"list_users","input":{"query":"a"}},` +
		`{"type":"tool_use","id":"toolu_b","name":"list_users","input":{"query":"b"}},` +
		`{"type":"tool_use","id":"toolu_c","name":"list_users","input":{"query":"c"}}],` +
		`"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`
	f := newFakeAnthropic(t, parallel, toolUseResponse("toolu_d", "submit_draft", `{"subject":"x"}`))
	handled := 0
	handle := func(context.Context, ToolCall) (json.RawMessage, error) {
		handled++
		return json.RawMessage(`[]`), nil
	}
	req := testRequest()
	req.MaxToolCalls = 1
	res, err := newTestClient(f, 10*time.Second).Extract(context.Background(), req, handle)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if handled != 1 || res.ToolCalls != 1 {
		t.Errorf("handler called %d times, ToolCalls = %d, want 1 and 1", handled, res.ToolCalls)
	}
	jsonEqual(t, res.Final, `{"subject":"x"}`)

	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	second := reqs[1]
	blocks := lastMessageBlocks(t, second)
	if len(blocks) != 3 {
		t.Fatalf("second request last message = %v, want three tool_results", blocks)
	}
	for i, wantErr := range []bool{false, true, true} {
		if blocks[i]["type"] != "tool_result" {
			t.Errorf("block %d type = %v, want tool_result", i, blocks[i]["type"])
		}
		if isErr, _ := blocks[i]["is_error"].(bool); isErr != wantErr {
			t.Errorf("block %d is_error = %v, want %v", i, isErr, wantErr)
		}
	}
	tc, _ := second["tool_choice"].(map[string]any)
	if tc["type"] != "tool" || tc["name"] != "submit_draft" {
		t.Errorf("second tool_choice = %v, want tool submit_draft", second["tool_choice"])
	}
	tools, _ := second["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("second request tools = %v, want only submit_draft", tools)
	}
	if tool, _ := tools[0].(map[string]any); tool["name"] != "submit_draft" {
		t.Errorf("second request tool = %v, want submit_draft", tools[0])
	}
}

func TestClient_Extract_MaxTokens_ErrTruncated(t *testing.T) {
	truncated := strings.Replace(toolUseResponse("toolu_1", "submit_draft", `{"subject":"x"}`),
		`"stop_reason":"tool_use"`, `"stop_reason":"max_tokens"`, 1)
	f := newFakeAnthropic(t, truncated)
	handle := func(context.Context, ToolCall) (json.RawMessage, error) { return nil, nil }
	res, err := newTestClient(f, 10*time.Second).Extract(context.Background(), testRequest(), handle)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if len(res.Final) != 0 {
		t.Errorf("Final = %s, want empty", res.Final)
	}
	if len(res.Turns) != 2 {
		t.Errorf("Turns = %d, want 2 (kept for the audit row)", len(res.Turns))
	}
}

func TestClient_Extract_EmptyHandlerOutput_SendsEmptyArray(t *testing.T) {
	f := newFakeAnthropic(t,
		toolUseResponse("toolu_1", "list_users", `{"query":"ben"}`),
		toolUseResponse("toolu_2", "submit_draft", `{"subject":"x"}`),
	)
	handle := func(context.Context, ToolCall) (json.RawMessage, error) { return nil, nil }
	if _, err := newTestClient(f, 10*time.Second).Extract(context.Background(), testRequest(), handle); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	blocks := lastMessageBlocks(t, f.requests()[1])
	if len(blocks) != 1 || mustJSON(t, blocks[0]["content"]) != `[{"text":"[]","type":"text"}]` {
		t.Errorf("tool_result = %v, want content text []", blocks)
	}
}

func TestClient_Extract_Timeout(t *testing.T) {
	f := newFakeAnthropic(t, toolUseResponse("toolu_1", "submit_draft", `{"subject":"x"}`))
	f.mu.Lock()
	f.delay = 2 * time.Second
	f.mu.Unlock()
	handle := func(context.Context, ToolCall) (json.RawMessage, error) { return nil, nil }
	start := time.Now()
	_, err := newTestClient(f, 50*time.Millisecond).Extract(context.Background(), testRequest(), handle)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Extract took %v, want it to stop near the timeout", elapsed)
	}
}
