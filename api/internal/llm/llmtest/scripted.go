// Package llmtest provides a scripted llm.Extractor for other packages' tests.
package llmtest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/monroeresidential/fieldassistant/api/internal/llm"
)

// Step is one move of the scripted "model". Exactly one of its forms is used:
// Err makes Extract fail; Tool calls that lookup tool with Input (JSON) and
// records the handler's result; Final ends the conversation with Final (JSON)
// as Result.Final.
type Step struct {
	Tool  string
	Input string
	Final string
	Err   error
}

// Scripted is an llm.Extractor that plays Steps in order on every Extract
// call. It records each Request in Requests and each handler result in
// Results; a handler error is recorded as {"error": "<text>"}, which is what
// the real client reports to the model.
type Scripted struct {
	Steps []Step
	// Model is reported as Result.Model.
	Model string

	mu       sync.Mutex
	Requests []llm.Request
	Results  []json.RawMessage
}

var _ llm.Extractor = (*Scripted)(nil)

// Extract plays the script. It returns llm.ErrNoFinal if the steps run out
// before a Final step.
func (s *Scripted) Extract(ctx context.Context, req llm.Request, handle llm.ToolHandler) (llm.Result, error) {
	s.mu.Lock()
	s.Requests = append(s.Requests, req)
	s.mu.Unlock()

	res := llm.Result{Model: s.Model}
	res.Turns = append(res.Turns, turn("user", []map[string]any{{"type": "text", "text": req.User}}))
	for i, step := range s.Steps {
		switch {
		case step.Err != nil:
			return res, step.Err
		case step.Tool != "":
			call := llm.ToolCall{ID: fmt.Sprintf("toolu_%d", i+1), Name: step.Tool, Input: json.RawMessage(step.Input)}
			res.Turns = append(res.Turns, turn("assistant", []map[string]any{toolUse(call)}))
			res.ToolCalls++
			out, err := handle(ctx, call)
			isErr := err != nil
			if isErr {
				out, _ = json.Marshal(map[string]string{"error": err.Error()})
			}
			s.mu.Lock()
			s.Results = append(s.Results, out)
			s.mu.Unlock()
			res.Turns = append(res.Turns, turn("user", []map[string]any{{
				"type": "tool_result", "tool_use_id": call.ID, "content": string(out), "is_error": isErr,
			}}))
		default:
			call := llm.ToolCall{ID: fmt.Sprintf("toolu_%d", i+1), Name: req.FinalTool, Input: json.RawMessage(step.Final)}
			res.Turns = append(res.Turns, turn("assistant", []map[string]any{toolUse(call)}))
			res.Final = json.RawMessage(step.Final)
			return res, nil
		}
	}
	return res, llm.ErrNoFinal
}

func toolUse(call llm.ToolCall) map[string]any {
	return map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Input}
}

func turn(role string, content []map[string]any) llm.Turn {
	b, err := json.Marshal(content)
	if err != nil {
		panic(fmt.Sprintf("llmtest: encode turn: %v", err))
	}
	return llm.Turn{Role: role, Content: b}
}
