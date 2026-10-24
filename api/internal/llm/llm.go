// Package llm runs feature-agnostic extraction calls against Claude: one
// conversation in which the model may call read-only lookup tools and must
// finish by calling a designated final tool whose input is the extraction.
// Only client.go knows about the Anthropic SDK; callers depend on Extractor.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ErrNoFinal means the model ended its turn without calling the final tool.
var ErrNoFinal = errors.New("llm: model stopped without calling the final tool")

// ErrTruncated means a response stopped at max_tokens. Its tool_use input
// may be cut off, so no block of that response is used; the returned error
// wraps ErrTruncated and names the stop reason.
var ErrTruncated = errors.New("llm: response truncated at max_tokens")

// ErrToolBudget means the model kept calling lookup tools after the tool
// call budget ran out, even when forced to call the final tool.
var ErrToolBudget = errors.New("llm: tool call budget exhausted")

// Tool is a tool offered to the model, described by a JSON schema.
type Tool struct {
	Name        string
	Description string
	// InputSchema is a JSON schema object ("type": "object", "properties", ...).
	InputSchema map[string]any
}

// ToolCall is one tool_use block the model emitted.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolHandler answers a lookup tool call. An error is reported back to the
// model as a tool_result with is_error set, not as a failure of Extract.
type ToolHandler func(ctx context.Context, call ToolCall) (json.RawMessage, error)

// Request is one extraction: a system prompt, the user text, the tools on
// offer, and the name of the tool whose input is the final answer.
type Request struct {
	System    string
	User      string
	Tools     []Tool
	FinalTool string
	// MaxToolCalls caps how many lookup tool calls are answered; past it the
	// model is forced to call FinalTool.
	MaxToolCalls int
}

// Turn is one message of the conversation as sent or received, for the
// audit row. Role is "user" or "assistant"; Content is the raw JSON array
// of content blocks.
type Turn struct {
	Role    string
	Content json.RawMessage
}

// Result is the outcome of an extraction. Turns, ToolCalls and token counts
// are filled in as far as the conversation got, even when Extract fails.
type Result struct {
	// Final is the input of the final tool call.
	Final json.RawMessage
	Turns []Turn
	// ToolCalls counts the lookup tool calls that were answered.
	ToolCalls int
	// Model is the model the API reported, or the configured one if absent.
	Model        string
	Duration     time.Duration
	InputTokens  int
	OutputTokens int
}

// Extractor runs an extraction conversation, answering lookup tool calls
// with handle.
type Extractor interface {
	Extract(ctx context.Context, req Request, handle ToolHandler) (Result, error)
}
