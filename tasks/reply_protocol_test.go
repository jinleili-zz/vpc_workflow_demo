package tasks

import (
	"context"
	"testing"

	"github.com/jinleili-zz/nsp-platform/taskqueue"

	"workflow_qoder/internal/orchestration"
)

func TestBuildReplyPayloadV2PreservesStableWorkflowIdentity(t *testing.T) {
	task := &taskqueue.Task{
		Type: "create_vrf_on_switch",
		Metadata: map[string]string{
			orchestration.MetadataKeyProtocolVersion: "2",
			orchestration.MetadataKeyOperationID:     "operation-1",
			orchestration.MetadataKeyRootOperationID: "root-1",
			orchestration.MetadataKeyWorkflowID:      "workflow-1",
			orchestration.MetadataKeyTaskID:          "task-1",
			orchestration.MetadataKeyGeneration:      "3",
			orchestration.MetadataKeyAttempt:         "0",
			orchestration.MetadataKeyStepName:        "create_vrf_on_switch",
			orchestration.MetadataKeyStepOrdinal:     "1",
			orchestration.MetadataKeyOperationKey:    "switch:create_vrf_on_switch:resource-1:gen:3",
			orchestration.MetadataKeyDesiredHash:     "desired-hash-1",
			orchestration.MetadataKeyResourceID:      "resource-1",
			orchestration.MetadataKeyResourceType:    "vpc",
			orchestration.MetadataKeyStepIndex:       "0",
			orchestration.MetadataKeyTotalSteps:      "2",
		},
	}

	first, err := buildReplyPayload(context.Background(), task, orchestration.ReplyStatusSuccess, map[string]any{"ok": true}, "")
	if err != nil {
		t.Fatalf("build first reply: %v", err)
	}
	second, err := buildReplyPayload(context.Background(), task, orchestration.ReplyStatusSuccess, map[string]any{"ok": true}, "")
	if err != nil {
		t.Fatalf("build second reply: %v", err)
	}
	if first.ProtocolVersion != orchestration.TaskProtocolVersion ||
		first.OperationID != "operation-1" || first.RootOperationID != "root-1" ||
		first.WorkflowID != "workflow-1" || first.TaskID != "task-1" ||
		first.Generation != 3 || first.Attempt != 0 || first.EventID == "" {
		t.Fatalf("reply identity = %#v", first)
	}
	if first.StepOrdinal != 1 || first.OperationKey != "switch:create_vrf_on_switch:resource-1:gen:3" || first.DesiredHash != "desired-hash-1" {
		t.Fatalf("reply command identity = %#v", first)
	}
	if first.EventID != second.EventID {
		t.Fatalf("duplicate execution reply event IDs differ: %s != %s", first.EventID, second.EventID)
	}
}
