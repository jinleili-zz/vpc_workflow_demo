package operation

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestServiceCanonicalizesRequestsBeforeBegin(t *testing.T) {
	db := openOperationTestDB(t)
	owner := "operation-service-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	service := NewService(NewRepository(db))

	request := BeginRequest{
		OwnerService:   owner,
		CallerScope:    "caller-a",
		RouteScope:     "POST /api/v1/vpc",
		OperationType:  "create_vpc",
		TargetScope:    "region-a/vpc-a",
		IdempotencyKey: "service-key",
		Payload:        map[string]any{"vpc_name": "vpc-a", "vlan_id": 101},
		ResourceType:   "vpc",
	}
	first, decision, err := service.Begin(context.Background(), request)
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin first: decision=%q err=%v", decision, err)
	}

	request.Payload = []byte(`{"vlan_id":101,"vpc_name":"vpc-a"}`)
	replayed, decision, err := service.Begin(context.Background(), request)
	if err != nil || decision != DecisionReplay {
		t.Fatalf("begin replay: decision=%q err=%v", decision, err)
	}
	if replayed.OperationID != first.OperationID {
		t.Fatalf("replayed operation ID = %s, want %s", replayed.OperationID, first.OperationID)
	}
}

func TestServiceRejectsInvalidIdempotencyKey(t *testing.T) {
	service := NewService(nil)
	for _, key := range []string{"", "line\nbreak", string([]byte{0x7f})} {
		request := BeginRequest{
			OwnerService: ownerForValidationTest,
			CallerScope:  "caller-a", RouteScope: "POST /api/v1/vpc",
			OperationType: "create_vpc", TargetScope: "region-a/vpc-a",
			IdempotencyKey: key, Payload: map[string]any{"vpc_name": "vpc-a"}, ResourceType: "vpc",
		}
		if _, _, err := service.Begin(context.Background(), request); err == nil {
			t.Fatalf("invalid idempotency key %q accepted", key)
		}
	}
}

const ownerForValidationTest = "operation-validation-test"
