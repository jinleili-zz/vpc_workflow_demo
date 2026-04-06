package asynqbroker

import (
	"context"
	"encoding/json"

	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/taskqueue"
	"github.com/jinleili-zz/nsp-platform/trace"
)

// taskEnvelope wraps the original task payload with trace metadata for
// transparent propagation across the message queue boundary.
// Version field (always 1) is used to reliably detect envelope format,
// avoiding false positives from business payloads that may contain a "payload" key.
type taskEnvelope struct {
	Version int               `json:"_v"`
	TraceID string            `json:"_tid,omitempty"`
	SpanID  string            `json:"_sid,omitempty"` // publisher's SpanId -> consumer's ParentSpanId
	Sampled bool              `json:"_smpl"`
	ReplyTo *json.RawMessage  `json:"_rto,omitempty"`
	Meta    map[string]string `json:"_meta,omitempty"`
	Payload []byte            `json:"payload"`
}

// wrapWithTrace extracts TraceContext from ctx and wraps the payload into an
// envelope. If neither trace, reply, nor metadata is present, or serialization
// fails, the original payload is returned unchanged (graceful degradation).
func wrapWithTrace(ctx context.Context, payload []byte, reply *taskqueue.ReplySpec, metadata map[string]string) []byte {
	tc, ok := trace.TraceFromContext(ctx)
	if (!ok || tc == nil) && reply == nil && len(metadata) == 0 {
		return payload
	}

	env := taskEnvelope{
		Version: 1,
		Payload: payload,
	}
	if ok && tc != nil {
		env.TraceID = tc.TraceID
		env.SpanID = tc.SpanId
		env.Sampled = tc.Sampled
	}
	if reply != nil {
		replyJSON, err := json.Marshal(reply)
		if err != nil {
			return payload
		}
		raw := json.RawMessage(replyJSON)
		env.ReplyTo = &raw
	}
	if len(metadata) > 0 {
		env.Meta = cloneMetadata(metadata)
	}

	data, err := json.Marshal(env)
	if err != nil {
		// Degradation: return original payload so message delivery is not affected.
		return payload
	}
	return data
}

// unwrapEnvelope splits an asynq payload into the original business payload
// and envelope metadata. If the data is not in envelope format (legacy messages),
// it returns the original payload with nil reply and empty metadata for
// backward compatibility.
func unwrapEnvelope(data []byte) (payload []byte, traceMeta map[string]string, reply *taskqueue.ReplySpec, businessMeta map[string]string) {
	var env taskEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Version != 1 {
		return data, nil, nil, map[string]string{}
	}

	if env.TraceID != "" {
		tc := &trace.TraceContext{
			TraceID: env.TraceID,
			SpanId:  env.SpanID,
			Sampled: env.Sampled,
		}
		traceMeta = trace.MetadataFromTraceContext(tc)
	}
	if env.ReplyTo != nil {
		var decoded taskqueue.ReplySpec
		if err := json.Unmarshal(*env.ReplyTo, &decoded); err == nil {
			reply = &decoded
		}
	}
	if len(env.Meta) > 0 {
		businessMeta = cloneMetadata(env.Meta)
	} else {
		businessMeta = map[string]string{}
	}
	return env.Payload, traceMeta, reply, businessMeta
}

// injectTraceFromMetadata restores a TraceContext from metadata and injects it
// into ctx. Both trace and logger context keys are set so that
// logger.InfoContext(ctx, ...) automatically includes trace_id and span_id.
// If metadata is empty or has no valid trace_id, the original ctx is returned.
func injectTraceFromMetadata(ctx context.Context, metadata map[string]string) context.Context {
	// Delegate to TraceFromMetadata for trace context construction (avoids code duplication)
	tc := trace.TraceFromMetadata(metadata, trace.GetInstanceId())
	if tc == nil {
		return ctx
	}

	ctx = trace.ContextWithTrace(ctx, tc)
	ctx = logger.ContextWithTraceID(ctx, tc.TraceID)
	ctx = logger.ContextWithSpanID(ctx, tc.SpanId)
	return ctx
}

func cloneMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
