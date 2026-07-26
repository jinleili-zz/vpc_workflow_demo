package tasks

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/orchestration"
)

func TestValidateTaskProtocolRejectsInvalidV2BeforeHandler(t *testing.T) {
	payload := []byte(`{"vpc_name":"vpc-a"}`)
	desiredHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	validMetadata := map[string]string{
		orchestration.MetadataKeyProtocolVersion: "2",
		orchestration.MetadataKeyEventID:         "event-1",
		orchestration.MetadataKeyOperationID:     "operation-1",
		orchestration.MetadataKeyRootOperationID: "root-1",
		orchestration.MetadataKeyWorkflowID:      "workflow-1",
		orchestration.MetadataKeyTaskID:          "task-1",
		orchestration.MetadataKeyGeneration:      "1",
		orchestration.MetadataKeyAttempt:         "0",
		orchestration.MetadataKeyStepName:        "create_vrf_on_switch",
		orchestration.MetadataKeyStepOrdinal:     "1",
		orchestration.MetadataKeyOperationKey:    "switch:create_vrf_on_switch:resource-1:gen:1",
		orchestration.MetadataKeyDesiredHash:     desiredHash,
		orchestration.MetadataKeyDeviceType:      "switch",
		orchestration.MetadataKeyResourceID:      "resource-1",
		orchestration.MetadataKeyResourceType:    "vpc",
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "missing operation key", mutate: func(metadata map[string]string) { delete(metadata, orchestration.MetadataKeyOperationKey) }},
		{name: "wrong desired hash", mutate: func(metadata map[string]string) { metadata[orchestration.MetadataKeyDesiredHash] = "wrong" }},
		{name: "step mismatch", mutate: func(metadata map[string]string) { metadata[orchestration.MetadataKeyStepName] = "other_step" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			metadata := make(map[string]string, len(validMetadata))
			for key, value := range validMetadata {
				metadata[key] = value
			}
			test.mutate(metadata)
			called := false
			handler := ValidateTaskProtocol(func(context.Context, *taskqueue.Task) error {
				called = true
				return nil
			})
			err := handler(context.Background(), &taskqueue.Task{Type: "create_vrf_on_switch", Payload: payload, Metadata: metadata})
			if err == nil || called {
				t.Fatalf("invalid v2 task err=%v handler_called=%v", err, called)
			}
		})
	}
}

func TestValidateTaskProtocolAllowsValidV2(t *testing.T) {
	payload := []byte(`{"vpc_name":"vpc-a"}`)
	desiredHash := fmt.Sprintf("%x", sha256.Sum256(payload))
	called := false
	handler := ValidateTaskProtocol(func(context.Context, *taskqueue.Task) error {
		called = true
		return nil
	})
	err := handler(context.Background(), &taskqueue.Task{
		Type:    "create_vrf_on_switch",
		Payload: payload,
		Metadata: map[string]string{
			orchestration.MetadataKeyProtocolVersion: "2",
			orchestration.MetadataKeyEventID:         "event-1",
			orchestration.MetadataKeyOperationID:     "operation-1",
			orchestration.MetadataKeyRootOperationID: "root-1",
			orchestration.MetadataKeyWorkflowID:      "workflow-1",
			orchestration.MetadataKeyTaskID:          "task-1",
			orchestration.MetadataKeyGeneration:      "1",
			orchestration.MetadataKeyAttempt:         "0",
			orchestration.MetadataKeyStepName:        "create_vrf_on_switch",
			orchestration.MetadataKeyStepOrdinal:     "1",
			orchestration.MetadataKeyOperationKey:    "switch:create_vrf_on_switch:resource-1:gen:1",
			orchestration.MetadataKeyDesiredHash:     desiredHash,
			orchestration.MetadataKeyDeviceType:      "switch",
			orchestration.MetadataKeyResourceID:      "resource-1",
			orchestration.MetadataKeyResourceType:    "vpc",
		},
	})
	if err != nil || !called {
		t.Fatalf("valid v2 task err=%v handler_called=%v", err, called)
	}
}

func TestValidateTaskProtocolRejectsLegacyByDefault(t *testing.T) {
	t.Setenv("NSP_WORKFLOW_V1_REPLY_ENABLED", "")
	called := false
	handler := ValidateTaskProtocol(func(context.Context, *taskqueue.Task) error {
		called = true
		return nil
	})
	err := handler(context.Background(), &taskqueue.Task{Type: "legacy", Metadata: map[string]string{}})
	if err == nil || called {
		t.Fatalf("legacy protocol err=%v handler_called=%v", err, called)
	}
}

func TestValidateTaskProtocolAllowsLegacyOnlyWhenExplicitlyEnabled(t *testing.T) {
	t.Setenv("NSP_WORKFLOW_V1_REPLY_ENABLED", "true")
	called := false
	handler := ValidateTaskProtocol(func(context.Context, *taskqueue.Task) error {
		called = true
		return nil
	})
	if err := handler(context.Background(), &taskqueue.Task{Type: "legacy", Metadata: map[string]string{}}); err != nil || !called {
		t.Fatalf("legacy protocol err=%v handler_called=%v", err, called)
	}
}
