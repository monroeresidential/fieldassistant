package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// maxTokens bounds each model response.
const maxTokens = 4096

// defaultMaxRetries matches the SDK default.
const defaultMaxRetries = 2

// Client is the Extractor backed by the Anthropic Messages API.
type Client struct {
	api        anthropic.Client
	model      string
	timeout    time.Duration
	maxRetries int
}

var _ Extractor = (*Client)(nil)

// NewClient returns a Client for model. An empty baseURL means the SDK
// default. timeout bounds each Extract call as a whole; zero means none.
func NewClient(apiKey, model, baseURL string, timeout time.Duration) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{
		api:        anthropic.NewClient(opts...),
		model:      model,
		timeout:    timeout,
		maxRetries: defaultMaxRetries,
	}
}

// Extract runs the tool loop: the model is offered every tool with
// tool_choice "any"; lookup calls are answered with handle until the model
// calls req.FinalTool, whose input becomes Result.Final. Once the budget of
// req.MaxToolCalls is spent, the next turn offers only FinalTool and forces it.
func (c *Client) Extract(ctx context.Context, req Request, handle ToolHandler) (res Result, err error) {
	start := time.Now()
	defer func() { res.Duration = time.Since(start) }()
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	allTools, finalTool, err := toolParams(req)
	if err != nil {
		return res, err
	}
	res.Model = c.model

	first := anthropic.NewUserMessage(anthropic.NewTextBlock(req.User))
	messages := []anthropic.MessageParam{first}
	if err := appendTurn(&res, "user", first.Content); err != nil {
		return res, err
	}

	forced := false
	for {
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(c.model),
			MaxTokens: maxTokens,
			System:    []anthropic.TextBlockParam{{Text: req.System}},
			Messages:  messages,
		}
		if forced {
			params.Tools = []anthropic.ToolUnionParam{finalTool}
			params.ToolChoice = anthropic.ToolChoiceParamOfTool(req.FinalTool)
		} else {
			params.Tools = allTools
			params.ToolChoice = anthropic.ToolChoiceUnionParam{OfAny: &anthropic.ToolChoiceAnyParam{}}
		}

		msg, err := c.api.Messages.New(ctx, params, option.WithMaxRetries(c.maxRetries))
		if err != nil {
			return res, fmt.Errorf("llm: messages call: %w", err)
		}
		if msg.Model != "" {
			res.Model = string(msg.Model)
		}
		res.InputTokens += int(msg.Usage.InputTokens)
		res.OutputTokens += int(msg.Usage.OutputTokens)
		res.Turns = append(res.Turns, Turn{Role: "assistant", Content: rawContent(msg.Content)})
		messages = append(messages, msg.ToParam())

		for _, block := range msg.Content {
			if block.Type == "tool_use" && block.Name == req.FinalTool {
				res.Final = append(json.RawMessage(nil), block.Input...)
				return res, nil
			}
		}

		var results []anthropic.ContentBlockParamUnion
		for _, block := range msg.Content {
			if block.Type != "tool_use" {
				continue
			}
			if forced {
				return res, ErrToolBudget
			}
			if res.ToolCalls >= req.MaxToolCalls {
				// Budget spent: answer this call with an error and force the
				// final tool on the next turn.
				forced = true
				results = append(results, anthropic.NewToolResultBlock(block.ID,
					fmt.Sprintf("%v; call %s now with what you have", ErrToolBudget, req.FinalTool), true))
				continue
			}
			res.ToolCalls++
			out, herr := handle(ctx, ToolCall{ID: block.ID, Name: block.Name, Input: block.Input})
			if herr != nil {
				results = append(results, anthropic.NewToolResultBlock(block.ID, herr.Error(), true))
				continue
			}
			results = append(results, anthropic.NewToolResultBlock(block.ID, string(out), false))
		}
		if len(results) == 0 {
			return res, fmt.Errorf("%w (stop reason %q)", ErrNoFinal, msg.StopReason)
		}

		next := anthropic.NewUserMessage(results...)
		messages = append(messages, next)
		if err := appendTurn(&res, "user", next.Content); err != nil {
			return res, err
		}
	}
}

// toolParams converts the request tools to SDK params and returns the final
// tool's param on its own for forced turns.
func toolParams(req Request) ([]anthropic.ToolUnionParam, anthropic.ToolUnionParam, error) {
	var all []anthropic.ToolUnionParam
	var final anthropic.ToolUnionParam
	found := false
	for _, t := range req.Tools {
		schema := anthropic.ToolInputSchemaParam{ExtraFields: map[string]any{}}
		for k, v := range t.InputSchema {
			switch k {
			case "type":
			case "properties":
				schema.Properties = v
			default:
				schema.ExtraFields[k] = v
			}
		}
		p := anthropic.ToolUnionParamOfTool(schema, t.Name)
		if t.Description != "" {
			p.OfTool.Description = anthropic.String(t.Description)
		}
		all = append(all, p)
		if t.Name == req.FinalTool {
			final, found = p, true
		}
	}
	if !found {
		return nil, final, fmt.Errorf("llm: final tool %q is not among the request tools", req.FinalTool)
	}
	return all, final, nil
}

// appendTurn records a sent message's content blocks as a Turn.
func appendTurn(res *Result, role string, content []anthropic.ContentBlockParamUnion) error {
	b, err := json.Marshal(content)
	if err != nil {
		return fmt.Errorf("llm: encode %s turn: %w", role, err)
	}
	res.Turns = append(res.Turns, Turn{Role: role, Content: b})
	return nil
}

// rawContent returns a received message's content blocks exactly as the API
// sent them, as a JSON array.
func rawContent(blocks []anthropic.ContentBlockUnion) json.RawMessage {
	parts := make([]string, len(blocks))
	for i, b := range blocks {
		parts[i] = b.RawJSON()
	}
	return json.RawMessage("[" + strings.Join(parts, ",") + "]")
}
