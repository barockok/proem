package proxy

import (
	"bytes"
	"encoding/json"
	"strings"
)

// maxUsageBuffer caps how much of a non-streaming body is held for usage
// parsing. Token counts live in a small JSON envelope; anything larger is a
// payload we have no reason to keep in memory.
const maxUsageBuffer = 1 << 20 // 1 MiB

// tokenUsage holds the billable counters reported by an upstream.
type tokenUsage struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
	Thinking      int
}

func (u tokenUsage) empty() bool {
	return u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheCreation == 0 && u.Thinking == 0
}

// usageEnvelope is the shape token counts arrive in, for both a whole
// non-streaming response and the events inside a stream.
type usageEnvelope struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	OutputTokensDetails      *struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

func (e usageEnvelope) thinking() int {
	if e.OutputTokensDetails == nil {
		return 0
	}
	return e.OutputTokensDetails.ThinkingTokens
}

// usageObserver extracts token counts from a response body as it passes by.
// It is a pure observer: it never alters, delays or reorders the bytes the
// client receives, and a body it cannot parse simply yields no usage.
//
// Both response shapes are handled, because the proxy must not care which one
// a client asked for:
//
//   - a single JSON object, whose top-level "usage" is read once at the end
//   - a text/event-stream, whose "message_start" carries the input and cache
//     counts and whose "message_delta" events carry a running output total
type usageObserver struct {
	sse bool

	// non-streaming: accumulate the body, bounded, and parse on Close
	buf      bytes.Buffer
	overflow bool

	// streaming: hold only the partial trailing line between writes
	pending []byte

	usage tokenUsage
}

func newUsageObserver(contentType string) *usageObserver {
	return &usageObserver{sse: strings.Contains(strings.ToLower(contentType), "text/event-stream")}
}

// Write consumes a chunk of the response body. It always reports success: a
// failure to understand the payload must never break the response.
func (o *usageObserver) Write(p []byte) (int, error) {
	if o.sse {
		o.consumeStream(p)
		return len(p), nil
	}
	if remaining := maxUsageBuffer - o.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			o.buf.Write(p[:remaining])
			o.overflow = true
		} else {
			o.buf.Write(p)
		}
	} else {
		o.overflow = true
	}
	return len(p), nil
}

// consumeStream scans complete lines, keeping any partial trailing line for
// the next chunk, since an event may be split across reads.
func (o *usageObserver) consumeStream(p []byte) {
	o.pending = append(o.pending, p...)
	for {
		idx := bytes.IndexByte(o.pending, '\n')
		if idx < 0 {
			return
		}
		line := o.pending[:idx]
		o.pending = o.pending[idx+1:]
		o.consumeLine(bytes.TrimSuffix(line, []byte("\r")))
	}
}

func (o *usageObserver) consumeLine(line []byte) {
	const prefix = "data:"
	if !bytes.HasPrefix(line, []byte(prefix)) {
		return
	}
	payload := bytes.TrimSpace(line[len(prefix):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}

	var event struct {
		Type    string `json:"type"`
		Message *struct {
			Usage *usageEnvelope `json:"usage"`
		} `json:"message"`
		Usage *usageEnvelope `json:"usage"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}

	// Upstreams disagree about which event carries which counter: some report
	// the input side in message_start and only the output in message_delta,
	// others leave message_start zeroed and report everything in the final
	// message_delta. Rather than assume a layout, merge usage from whichever
	// event supplies it.
	if event.Message != nil && event.Message.Usage != nil {
		o.mergeUsage(*event.Message.Usage)
	}
	if event.Usage != nil {
		o.mergeUsage(*event.Usage)
	}
}

// mergeUsage keeps the highest value seen for each counter. Every field is a
// running total for the message rather than an increment, so this converges on
// the final figure no matter which event reported it, and a zeroed placeholder
// never overwrites a real count.
func (o *usageObserver) mergeUsage(u usageEnvelope) {
	atLeast := func(dst *int, v int) {
		if v > *dst {
			*dst = v
		}
	}
	atLeast(&o.usage.Input, u.InputTokens)
	atLeast(&o.usage.Output, u.OutputTokens)
	atLeast(&o.usage.CacheRead, u.CacheReadInputTokens)
	atLeast(&o.usage.CacheCreation, u.CacheCreationInputTokens)
	atLeast(&o.usage.Thinking, u.thinking())
}

// Result returns what was observed. For a non-streaming body the parse happens
// here, once the whole envelope is available.
func (o *usageObserver) Result() tokenUsage {
	if o.sse {
		return o.usage
	}
	if o.overflow {
		// A truncated body cannot be parsed as JSON; report nothing rather
		// than guess.
		return tokenUsage{}
	}
	var envelope struct {
		Usage *usageEnvelope `json:"usage"`
	}
	if err := json.Unmarshal(o.buf.Bytes(), &envelope); err != nil || envelope.Usage == nil {
		return tokenUsage{}
	}
	return tokenUsage{
		Input:         envelope.Usage.InputTokens,
		Output:        envelope.Usage.OutputTokens,
		CacheRead:     envelope.Usage.CacheReadInputTokens,
		CacheCreation: envelope.Usage.CacheCreationInputTokens,
		Thinking:      envelope.Usage.thinking(),
	}
}
