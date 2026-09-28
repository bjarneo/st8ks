// Package assistant explains cluster issues with the Claude API. It is
// read-only: it gets cluster data as text and never runs commands.
package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

// Msg is one turn of a conversation.
type Msg struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Chunk carries streamed text to the frontend.
type Chunk struct {
	ID    string `json:"id"`
	Delta string `json:"delta,omitempty"`
	Done  bool   `json:"done,omitempty"`
	Err   string `json:"err,omitempty"`
}

const systemPrompt = `You are the assistant inside st8ks, a Kubernetes desktop client. You explain why workloads fail, from the cluster data in the conversation.

You have read-only access. You cannot run commands or change the cluster. The user applies every fix after they review it.

Write for an engineer who is on call and reads your answer once:
- Start with the cause in one or two sentences.
- Then give the evidence from the data as lines that start with "- ".
- Then give the fix on a line that starts with "Fix:". Name the exact object and field to change, and give a kubectl command when one fits.
- Use plain text. Do not use Markdown headings or tables.
- Keep the answer under 220 words.
- If the data does not show the cause, say what is missing and which read-only command would show it.`

// Assistant streams answers from Claude.
type Assistant struct {
	emit  func(name string, data any)
	key   func() string
	model func() string

	mu      sync.Mutex
	running map[string]context.CancelFunc
}

// New creates an assistant. key returns the API key from the settings, and
// the SDK falls back to ANTHROPIC_API_KEY and the ant CLI profile.
func New(emit func(string, any), key, model func() string) *Assistant {
	return &Assistant{emit: emit, key: key, model: model, running: map[string]context.CancelFunc{}}
}

func (a *Assistant) client() anthropic.Client {
	var opts []option.RequestOption
	if k := strings.TrimSpace(a.key()); k != "" {
		opts = append(opts, option.WithAPIKey(k))
	}
	return anthropic.NewClient(opts...)
}

// Ask streams an answer. contextText holds the cluster data for the issue.
// history holds the earlier turns, and question is the new user turn.
func (a *Assistant) Ask(id, contextText string, history []Msg, question string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.running[id] = cancel
	a.mu.Unlock()
	go func() {
		defer func() {
			a.mu.Lock()
			delete(a.running, id)
			a.mu.Unlock()
			cancel()
		}()
		a.stream(ctx, id, contextText, history, question)
	}()
}

// Cancel stops a running answer.
func (a *Assistant) Cancel(id string) {
	a.mu.Lock()
	cancel := a.running[id]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *Assistant) stream(ctx context.Context, id, contextText string, history []Msg, question string) {
	first := "Cluster data:\n\n" + contextText + "\n\nExplain the issue and suggest a fix."
	if len(history) == 0 && question != "" {
		// A question without an earlier answer goes in the first turn.
		first = "Cluster data:\n\n" + contextText + "\n\n" + question
		question = ""
	}
	msgs := []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(first))}
	for _, m := range history {
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		role := anthropic.BetaMessageParamRoleUser
		if m.Role == "assistant" {
			role = anthropic.BetaMessageParamRoleAssistant
		}
		msgs = append(msgs, anthropic.BetaMessageParam{Role: role, Content: []anthropic.BetaContentBlockParamUnion{anthropic.NewBetaTextBlock(m.Text)}})
	}
	if question != "" {
		msgs = append(msgs, anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(question)))
	}
	// A turn must alternate roles, so merge a trailing assistant turn away.
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == anthropic.BetaMessageParamRoleAssistant {
		msgs = msgs[:len(msgs)-1]
	}

	c := a.client()
	stream := c.Beta.Messages.NewStreaming(ctx, anthropic.BetaMessageNewParams{
		Model:     anthropic.Model(a.model()),
		MaxTokens: 64000,
		System:    []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
		Messages:  msgs,
		// A declined request is served again by the recommended model.
		Fallbacks: anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Betas:     []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
	})
	defer stream.Close()
	refused := false
	for stream.Next() {
		switch ev := stream.Current().AsAny().(type) {
		case anthropic.BetaRawContentBlockDeltaEvent:
			if d, ok := ev.Delta.AsAny().(anthropic.BetaTextDelta); ok && d.Text != "" {
				a.emit("ai", Chunk{ID: id, Delta: d.Text})
			}
		case anthropic.BetaRawMessageDeltaEvent:
			if ev.Delta.StopReason == anthropic.BetaStopReasonRefusal {
				refused = true
			}
		}
	}
	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			a.emit("ai", Chunk{ID: id, Done: true})
			return
		}
		a.emit("ai", Chunk{ID: id, Done: true, Err: describe(err)})
		return
	}
	if refused {
		a.emit("ai", Chunk{ID: id, Done: true, Err: "The model declined to answer this request."})
		return
	}
	a.emit("ai", Chunk{ID: id, Done: true})
}

func describe(err error) string {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "The Anthropic API rejected the key. Add a valid API key in the assistant settings."
		case http.StatusTooManyRequests:
			return "The Anthropic API rate limit is reached. Try again in a minute."
		case http.StatusNotFound:
			return "The model is not available for this API key. Choose another model in the assistant settings."
		}
		if apiErr.StatusCode >= 500 {
			return "The Anthropic API is not available right now. Try again later."
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "api key") || strings.Contains(msg, "API key") || strings.Contains(msg, "credentials") {
		return "No Anthropic API key is set. Add one in the assistant settings, or set ANTHROPIC_API_KEY."
	}
	return msg
}
