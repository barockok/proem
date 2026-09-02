package proxy

import (
	"strings"
	"testing"
)

func feed(o *usageObserver, chunks ...string) tokenUsage {
	for _, c := range chunks {
		_, _ = o.Write([]byte(c))
	}
	return o.Result()
}

func TestObserverJSON(t *testing.T) {
	o := newUsageObserver("application/json")
	got := feed(o, `{"usage":{"input_tokens":3,"output_tokens":4,`,
		`"cache_read_input_tokens":5,"cache_creation_input_tokens":6,`,
		`"output_tokens_details":{"thinking_tokens":2}}}`)
	want := tokenUsage{Input: 3, Output: 4, CacheRead: 5, CacheCreation: 6, Thinking: 2}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestObserverJSONWithoutUsage(t *testing.T) {
	o := newUsageObserver("application/json")
	if got := feed(o, `{"id":"m","content":[]}`); !got.empty() {
		t.Fatalf("expected no usage, got %+v", got)
	}
}

func TestObserverIgnoresUnparseableBody(t *testing.T) {
	o := newUsageObserver("application/json")
	if got := feed(o, "not json at all"); !got.empty() {
		t.Fatalf("got %+v", got)
	}
}

// An oversized body must not be buffered without bound, and a truncated body
// must report nothing rather than a wrong number.
func TestObserverBoundsMemory(t *testing.T) {
	o := newUsageObserver("application/json")
	got := feed(o, `{"usage":{"input_tokens":1},"pad":"`+strings.Repeat("x", maxUsageBuffer+1024)+`"}`)
	if !got.empty() {
		t.Fatalf("truncated body should report nothing, got %+v", got)
	}
	if o.buf.Len() > maxUsageBuffer {
		t.Fatalf("buffered %d bytes, cap is %d", o.buf.Len(), maxUsageBuffer)
	}
}

func TestObserverSSE(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":11,\"cache_read_input_tokens\":900,\"cache_creation_input_tokens\":7,\"output_tokens\":1}}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":20}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":55,\"output_tokens_details\":{\"thinking_tokens\":9}}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	)
	want := tokenUsage{Input: 11, Output: 55, CacheRead: 900, CacheCreation: 7, Thinking: 9}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// Events do not arrive aligned to reads, so a JSON payload split mid-line must
// still be understood.
func TestObserverSSESplitAcrossChunks(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		"event: message_start\ndata: {\"type\":\"message_start\",\"mess",
		"age\":{\"usage\":{\"input_tokens\":8,\"output_tokens\":1}}}",
		"\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usa",
		"ge\":{\"output_tokens\":33}}\n\n",
	)
	if got.Input != 8 || got.Output != 33 {
		t.Fatalf("got %+v", got)
	}
}

func TestObserverSSEHandlesCRLFAndDone(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":4}}}\r\n\r\n",
		"data: [DONE]\r\n\r\n",
	)
	if got.Input != 4 {
		t.Fatalf("got %+v", got)
	}
}

func TestObserverSSEIgnoresJunkLines(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		": keep-alive comment\n\n",
		"data: {not json}\n\n",
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":12}}\n\n",
	)
	if got.Output != 12 {
		t.Fatalf("got %+v", got)
	}
}

// Output counts in message_delta are cumulative, so a later smaller value must
// not lower the total.
func TestObserverSSEOutputIsMonotonic(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":50}}\n\n",
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":10}}\n\n",
	)
	if got.Output != 50 {
		t.Fatalf("got %+v", got)
	}
}

func TestObserverContentTypeDetection(t *testing.T) {
	if !newUsageObserver("text/event-stream; charset=utf-8").sse {
		t.Fatal("charset suffix should still be detected as a stream")
	}
	if newUsageObserver("application/json").sse {
		t.Fatal("json must not be treated as a stream")
	}
	if newUsageObserver("").sse {
		t.Fatal("empty content type must not be treated as a stream")
	}
}

func TestUsageEmpty(t *testing.T) {
	if !(tokenUsage{}).empty() {
		t.Fatal("zero usage should be empty")
	}
	if (tokenUsage{CacheRead: 1}).empty() {
		t.Fatal("cache-only usage is not empty")
	}
}

// Some upstreams zero out message_start and report every counter in the final
// message_delta. Accounting must not depend on that choice.
func TestObserverSSEUsageOnlyInDelta(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":0,"output_tokens":0}}}`+"\n\n",
		`data: {"type":"message_delta","usage":{"input_tokens":8,"output_tokens":64,`+
			`"cache_read_input_tokens":120,"cache_creation_input_tokens":4,`+
			`"output_tokens_details":{"thinking_tokens":61}}}`+"\n\n",
	)
	want := tokenUsage{Input: 8, Output: 64, CacheRead: 120, CacheCreation: 4, Thinking: 61}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

// The other convention: message_start carries the input side, message_delta
// only the output. A zeroed later field must not erase an earlier real one.
func TestObserverSSEUsageSplitAcrossEvents(t *testing.T) {
	o := newUsageObserver("text/event-stream")
	got := feed(o,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":500,"cache_read_input_tokens":90}}}`+"\n\n",
		`data: {"type":"message_delta","usage":{"input_tokens":0,"output_tokens":17}}`+"\n\n",
	)
	want := tokenUsage{Input: 500, Output: 17, CacheRead: 90}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
