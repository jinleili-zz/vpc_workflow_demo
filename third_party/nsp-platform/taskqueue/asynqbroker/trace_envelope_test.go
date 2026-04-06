package asynqbroker

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"
)

func TestWrapWithTrace_NoTraceInContext(t *testing.T) {
	payload := []byte(`{"task_id":"t1","resource_id":"r1"}`)
	ctx := context.Background()

	result := wrapWithTrace(ctx, payload, nil, nil)

	// Without trace, payload should be returned as-is.
	if string(result) != string(payload) {
		t.Errorf("expected original payload, got %s", result)
	}
}

func TestWrapWithTrace_WithReplyOnly(t *testing.T) {
	payload := []byte(`{"task":"reply"}`)

	result := wrapWithTrace(context.Background(), payload, &taskqueue.ReplySpec{Queue: "callback-q"}, nil)

	var env taskEnvelope
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if env.ReplyTo == nil {
		t.Fatal("expected reply envelope field")
	}

	var reply taskqueue.ReplySpec
	if err := json.Unmarshal(*env.ReplyTo, &reply); err != nil {
		t.Fatalf("failed to unmarshal reply: %v", err)
	}
	if reply.Queue != "callback-q" {
		t.Fatalf("unexpected reply queue: %s", reply.Queue)
	}
}

func TestWrapWithTrace_WithMetadataOnly(t *testing.T) {
	payload := []byte(`{"task":"meta"}`)
	metadata := map[string]string{"tenant": "acme"}

	result := wrapWithTrace(context.Background(), payload, nil, metadata)

	var env taskEnvelope
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if !reflect.DeepEqual(env.Meta, metadata) {
		t.Fatalf("unexpected metadata: %#v", env.Meta)
	}
}

func TestWrapWithTrace_WithNonJSONPayloadPreservesEnvelopeFields(t *testing.T) {
	payload := []byte("plain-text-payload")
	reply := &taskqueue.ReplySpec{Queue: "callback-q"}
	metadata := map[string]string{"tenant": "acme"}

	result := wrapWithTrace(context.Background(), payload, reply, metadata)
	if string(result) == string(payload) {
		t.Fatal("expected envelope payload, got raw payload")
	}

	decodedPayload, traceMeta, decodedReply, decodedMetadata := unwrapEnvelope(result)
	if string(decodedPayload) != string(payload) {
		t.Fatalf("unexpected payload after unwrap: %q", decodedPayload)
	}
	if traceMeta != nil {
		t.Fatalf("expected nil trace metadata, got %#v", traceMeta)
	}
	if decodedReply == nil || decodedReply.Queue != reply.Queue {
		t.Fatalf("unexpected reply after unwrap: %#v", decodedReply)
	}
	if !reflect.DeepEqual(decodedMetadata, metadata) {
		t.Fatalf("unexpected metadata after unwrap: %#v", decodedMetadata)
	}
}

func TestWrapWithTrace_WithTraceInContext(t *testing.T) {
	payload := []byte(`{"task_id":"t1","resource_id":"r1"}`)
	tc := &trace.TraceContext{
		TraceID:      "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:       "00f067aa0ba902b7",
		ParentSpanId: "",
		InstanceId:   "test-instance",
		Sampled:      true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	result := wrapWithTrace(ctx, payload, nil, nil)

	// Should be an envelope.
	var env taskEnvelope
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if env.Version != 1 {
		t.Errorf("expected Version=1, got %d", env.Version)
	}
	if env.TraceID != tc.TraceID {
		t.Errorf("expected TraceID=%s, got %s", tc.TraceID, env.TraceID)
	}
	if env.SpanID != tc.SpanId {
		t.Errorf("expected SpanID=%s, got %s", tc.SpanId, env.SpanID)
	}
	if !env.Sampled {
		t.Error("expected Sampled=true")
	}
	// Verify nested payload is intact.
	if string(env.Payload) != string(payload) {
		t.Errorf("expected original payload in envelope, got %s", env.Payload)
	}
}

func TestWrapWithTrace_SampledFalse(t *testing.T) {
	payload := []byte(`{"task_id":"t1"}`)
	tc := &trace.TraceContext{
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:  "00f067aa0ba902b7",
		Sampled: false,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	result := wrapWithTrace(ctx, payload, nil, nil)

	var env taskEnvelope
	if err := json.Unmarshal(result, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if env.Sampled {
		t.Error("expected Sampled=false")
	}
}

func TestUnwrapEnvelope_ValidEnvelope(t *testing.T) {
	original := []byte(`{"task_id":"t1","resource_id":"r1"}`)
	envelope, _ := json.Marshal(taskEnvelope{
		Version: 1,
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:  "00f067aa0ba902b7",
		Sampled: true,
		Payload: original,
	})

	payload, traceMeta, reply, businessMeta := unwrapEnvelope(envelope)

	if string(payload) != string(original) {
		t.Errorf("expected original payload, got %s", payload)
	}
	if traceMeta == nil {
		t.Fatal("expected non-nil metadata")
	}
	if traceMeta["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("unexpected trace_id: %s", traceMeta["trace_id"])
	}
	if traceMeta["span_id"] != "00f067aa0ba902b7" {
		t.Errorf("unexpected span_id: %s", traceMeta["span_id"])
	}
	if traceMeta["sampled"] != "1" {
		t.Errorf("expected sampled=1, got %s", traceMeta["sampled"])
	}
	if reply != nil {
		t.Fatalf("expected nil reply, got %#v", reply)
	}
	if len(businessMeta) != 0 {
		t.Fatalf("expected empty business metadata, got %#v", businessMeta)
	}
}

func TestUnwrapEnvelope_SampledFalse(t *testing.T) {
	original := []byte(`{"task_id":"t1"}`)
	envelope, _ := json.Marshal(taskEnvelope{
		Version: 1,
		TraceID: "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanID:  "00f067aa0ba902b7",
		Sampled: false,
		Payload: original,
	})

	_, traceMeta, _, _ := unwrapEnvelope(envelope)

	if traceMeta["sampled"] != "0" {
		t.Errorf("expected sampled=0, got %s", traceMeta["sampled"])
	}
}

func TestUnwrapEnvelope_WithReplyAndMetadata(t *testing.T) {
	original := []byte(`{"task":"roundtrip"}`)
	replyJSON := json.RawMessage(`{"queue":"callback-q"}`)
	envelope, _ := json.Marshal(taskEnvelope{
		Version: 1,
		ReplyTo: &replyJSON,
		Meta: map[string]string{
			"tenant": "acme",
			"region": "cn",
		},
		Payload: original,
	})

	payload, traceMeta, reply, businessMeta := unwrapEnvelope(envelope)

	if string(payload) != string(original) {
		t.Fatalf("unexpected payload: %s", payload)
	}
	if traceMeta != nil {
		t.Fatalf("expected nil trace metadata, got %#v", traceMeta)
	}
	if reply == nil || reply.Queue != "callback-q" {
		t.Fatalf("unexpected reply: %#v", reply)
	}
	if !reflect.DeepEqual(businessMeta, map[string]string{"tenant": "acme", "region": "cn"}) {
		t.Fatalf("unexpected business metadata: %#v", businessMeta)
	}
}

func TestUnwrapEnvelope_LegacyPayload(t *testing.T) {
	// Legacy payload without envelope wrapping.
	legacy := []byte(`{"task_id":"t1","resource_id":"r1","task_params":"{}"}`)

	payload, traceMeta, reply, businessMeta := unwrapEnvelope(legacy)

	if string(payload) != string(legacy) {
		t.Errorf("expected legacy payload returned as-is, got %s", payload)
	}
	if traceMeta != nil {
		t.Errorf("expected nil trace metadata for legacy payload, got %v", traceMeta)
	}
	if reply != nil {
		t.Errorf("expected nil reply for legacy payload, got %#v", reply)
	}
	if len(businessMeta) != 0 {
		t.Errorf("expected empty business metadata for legacy payload, got %#v", businessMeta)
	}
}

func TestUnwrapEnvelope_InvalidJSON(t *testing.T) {
	garbage := []byte(`not json at all`)

	payload, traceMeta, reply, businessMeta := unwrapEnvelope(garbage)

	if string(payload) != string(garbage) {
		t.Errorf("expected garbage returned as-is, got %s", payload)
	}
	if traceMeta != nil {
		t.Errorf("expected nil trace metadata for garbage, got %v", traceMeta)
	}
	if reply != nil {
		t.Errorf("expected nil reply for garbage, got %#v", reply)
	}
	if len(businessMeta) != 0 {
		t.Errorf("expected empty business metadata for garbage, got %#v", businessMeta)
	}
}

func TestUnwrapEnvelope_WrongVersion(t *testing.T) {
	data, _ := json.Marshal(map[string]interface{}{
		"_v":      99,
		"payload": "some data",
	})

	payload, traceMeta, reply, businessMeta := unwrapEnvelope(data)

	if string(payload) != string(data) {
		t.Errorf("expected original data returned for wrong version, got %s", payload)
	}
	if traceMeta != nil {
		t.Errorf("expected nil trace metadata for wrong version, got %v", traceMeta)
	}
	if reply != nil {
		t.Errorf("expected nil reply for wrong version, got %#v", reply)
	}
	if len(businessMeta) != 0 {
		t.Errorf("expected empty business metadata for wrong version, got %#v", businessMeta)
	}
}

func TestRoundTrip_WrapAndUnwrap(t *testing.T) {
	original := []byte(`{"task_id":"t1","resource_id":"r1","task_params":"{\"key\":\"value\"}"}`)
	tc := &trace.TraceContext{
		TraceID:      "4bf92f3577b34da6a3ce929d0e0e4736",
		SpanId:       "00f067aa0ba902b7",
		ParentSpanId: "1111111111111111",
		InstanceId:   "test-instance",
		Sampled:      true,
	}
	ctx := trace.ContextWithTrace(context.Background(), tc)

	replySpec := &taskqueue.ReplySpec{Queue: "callback-q"}
	metadata := map[string]string{"tenant": "acme"}
	wrapped := wrapWithTrace(ctx, original, replySpec, metadata)
	payload, traceMeta, reply, businessMeta := unwrapEnvelope(wrapped)

	if string(payload) != string(original) {
		t.Errorf("round-trip payload mismatch: got %s", payload)
	}
	if traceMeta["trace_id"] != tc.TraceID {
		t.Errorf("round-trip trace_id mismatch: got %s", traceMeta["trace_id"])
	}
	if traceMeta["span_id"] != tc.SpanId {
		t.Errorf("round-trip span_id mismatch: got %s", traceMeta["span_id"])
	}
	if reply == nil || reply.Queue != replySpec.Queue {
		t.Fatalf("round-trip reply mismatch: %#v", reply)
	}
	if !reflect.DeepEqual(businessMeta, metadata) {
		t.Fatalf("round-trip metadata mismatch: %#v", businessMeta)
	}
}

func TestInjectTraceFromMetadata_NilMetadata(t *testing.T) {
	ctx := context.Background()
	result := injectTraceFromMetadata(ctx, nil)

	// Should be the same ctx, no trace.
	if tc, ok := trace.TraceFromContext(result); ok && tc != nil {
		t.Error("expected no trace in ctx with nil metadata")
	}
}

func TestInjectTraceFromMetadata_EmptyTraceID(t *testing.T) {
	ctx := context.Background()
	metadata := map[string]string{
		"trace_id": "",
		"span_id":  "abc",
	}
	result := injectTraceFromMetadata(ctx, metadata)

	if tc, ok := trace.TraceFromContext(result); ok && tc != nil {
		t.Error("expected no trace in ctx with empty trace_id")
	}
}

func TestInjectTraceFromMetadata_Valid(t *testing.T) {
	ctx := context.Background()
	metadata := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id":  "00f067aa0ba902b7",
		"sampled":  "1",
	}

	result := injectTraceFromMetadata(ctx, metadata)

	// Verify trace.TraceContext is set.
	tc, ok := trace.TraceFromContext(result)
	if !ok || tc == nil {
		t.Fatal("expected TraceContext in ctx")
	}
	if tc.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("expected TraceID=%s, got %s", "4bf92f3577b34da6a3ce929d0e0e4736", tc.TraceID)
	}
	if tc.ParentSpanId != "00f067aa0ba902b7" {
		t.Errorf("expected ParentSpanId=%s, got %s", "00f067aa0ba902b7", tc.ParentSpanId)
	}
	// Consumer generates its own SpanId.
	if tc.SpanId == "" {
		t.Error("expected non-empty SpanId")
	}
	if tc.SpanId == "00f067aa0ba902b7" {
		t.Error("consumer SpanId should differ from publisher's SpanId")
	}
	if !tc.Sampled {
		t.Error("expected Sampled=true")
	}

	// Verify logger context keys are also set.
	if tid := logger.TraceIDFromContext(result); tid != tc.TraceID {
		t.Errorf("logger TraceID mismatch: expected %s, got %s", tc.TraceID, tid)
	}
	if sid := logger.SpanIDFromContext(result); sid != tc.SpanId {
		t.Errorf("logger SpanID mismatch: expected %s, got %s", tc.SpanId, sid)
	}
}

func TestInjectTraceFromMetadata_SampledFalse(t *testing.T) {
	ctx := context.Background()
	metadata := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id":  "00f067aa0ba902b7",
		"sampled":  "0",
	}

	result := injectTraceFromMetadata(ctx, metadata)

	tc, ok := trace.TraceFromContext(result)
	if !ok || tc == nil {
		t.Fatal("expected TraceContext in ctx")
	}
	if tc.Sampled {
		t.Error("expected Sampled=false when metadata sampled=0")
	}
}

func TestInjectTraceFromMetadata_SampledMissing(t *testing.T) {
	ctx := context.Background()
	metadata := map[string]string{
		"trace_id": "4bf92f3577b34da6a3ce929d0e0e4736",
		"span_id":  "00f067aa0ba902b7",
		// "sampled" is absent — should default to true.
	}

	result := injectTraceFromMetadata(ctx, metadata)

	tc, ok := trace.TraceFromContext(result)
	if !ok || tc == nil {
		t.Fatal("expected TraceContext in ctx")
	}
	if !tc.Sampled {
		t.Error("expected Sampled=true when sampled key is missing (default)")
	}
}
