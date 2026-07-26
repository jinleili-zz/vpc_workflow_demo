package operation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestRepositoryBeginSequentialReplayAndConflict(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-test-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })

	cmd := beginTestCommand(t, owner, "key-sequential", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a", "vlan_id": 101})
	first, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil {
		t.Fatalf("begin first: %v", err)
	}
	if decision != DecisionNew || first.OperationID == "" || first.RootOperationID != first.OperationID {
		t.Fatalf("first operation/decision = %#v/%q", first, decision)
	}

	replayed, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil {
		t.Fatalf("begin replay: %v", err)
	}
	if decision != DecisionReplay || replayed.OperationID != first.OperationID {
		t.Fatalf("replay operation/decision = %#v/%q, want ID %s", replayed, decision, first.OperationID)
	}

	conflicting := beginTestCommand(t, owner, cmd.IdempotencyKey, cmd.TargetScope, map[string]any{"vpc_name": "vpc-a", "vlan_id": 202})
	conflictOp, decision, err := repo.Begin(context.Background(), conflicting)
	if err != nil {
		t.Fatalf("begin conflict: %v", err)
	}
	if decision != DecisionConflict || conflictOp.OperationID != first.OperationID {
		t.Fatalf("conflict operation/decision = %#v/%q, want existing ID %s", conflictOp, decision, first.OperationID)
	}
	if got := countTestOperations(t, db, owner); got != 1 {
		t.Fatalf("operation rows = %d, want 1", got)
	}
}

func TestRepositoryBeginTargetTxCommitsClaimWithCallerTransaction(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-target-tx-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	cmd := beginTestCommand(t, owner, "target-tx-key", "region-a/az-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})
	cmd.ResourceID = uuid.NewString()

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	if _, decision, err := repo.BeginTargetTx(t.Context(), tx, cmd); err != nil || decision != DecisionNew {
		_ = tx.Rollback()
		t.Fatalf("begin target in transaction: decision=%s err=%v", decision, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback transaction: %v", err)
	}
	if got := countTestOperations(t, db, owner); got != 0 {
		t.Fatalf("operations after rollback = %d, want 0", got)
	}

	tx, err = db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin commit transaction: %v", err)
	}
	if _, decision, err := repo.BeginTargetTx(t.Context(), tx, cmd); err != nil || decision != DecisionNew {
		_ = tx.Rollback()
		t.Fatalf("begin committed target: decision=%s err=%v", decision, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	var active bool
	if err := db.QueryRow(`SELECT active FROM orchestration_target_claims WHERE owner_service = $1 AND resource_type = $2 AND target_scope = $3`, owner, cmd.ResourceType, cmd.TargetScope).Scan(&active); err != nil {
		t.Fatalf("load committed target claim: %v", err)
	}
	if !active {
		t.Fatal("committed target claim is inactive")
	}
}

func TestRetiringTargetRejectsNewCreateUntilReleased(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-retiring-" + uuid.NewString()
	target := "region-a/az-a/vpc-a"
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })

	first, decision, err := service.BeginTarget(t.Context(), BeginRequest{
		OwnerService: owner, CallerScope: "caller-a", RouteScope: "POST /api/v1/vpc",
		OperationType: "create_vpc", TargetScope: target, IdempotencyKey: uuid.NewString(),
		Payload:      map[string]any{"vpc_name": "vpc-a", "vlan_id": 100},
		ResourceType: "vpc", ResourceID: uuid.NewString(),
	})
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin target: decision=%s err=%v", decision, err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET status = 'succeeded', completed_at = NOW() WHERE operation_id = $1`, first.OperationID); err != nil {
		t.Fatalf("complete create: %v", err)
	}
	if err := service.MarkTargetRetiring(t.Context(), owner, "vpc", target); err != nil {
		t.Fatalf("mark target retiring: %v", err)
	}

	busy, decision, err := service.BeginTarget(t.Context(), BeginRequest{
		OwnerService: owner, CallerScope: "caller-a", RouteScope: "POST /api/v1/vpc",
		OperationType: "create_vpc", TargetScope: target, IdempotencyKey: uuid.NewString(),
		Payload:      map[string]any{"vpc_name": "vpc-a", "vlan_id": 100},
		ResourceType: "vpc", ResourceID: uuid.NewString(),
	})
	if err != nil || decision != DecisionResourceBusy {
		t.Fatalf("create against retiring target: op=%#v decision=%s err=%v", busy, decision, err)
	}
	if busy.ErrorCode != ErrResourceOperationInProgress.Error() {
		t.Fatalf("busy error code = %q", busy.ErrorCode)
	}

	if err := service.ReleaseTarget(t.Context(), owner, "vpc", target); err != nil {
		t.Fatalf("release target: %v", err)
	}
	next, decision, err := service.BeginTarget(t.Context(), BeginRequest{
		OwnerService: owner, CallerScope: "caller-a", RouteScope: "POST /api/v1/vpc",
		OperationType: "create_vpc", TargetScope: target, IdempotencyKey: uuid.NewString(),
		Payload:      map[string]any{"vpc_name": "vpc-a", "vlan_id": 101},
		ResourceType: "vpc", ResourceID: uuid.NewString(),
	})
	if err != nil || decision != DecisionNew || next.Generation != first.Generation+1 {
		t.Fatalf("create after release: op=%#v decision=%s err=%v", next, decision, err)
	}
}

func TestMarkTargetRetiringCancelsInFlightCreate(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-delete-wins-" + uuid.NewString()
	target := "region-a/az-a/subnet-a"
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, decision, err := service.BeginTarget(t.Context(), BeginRequest{
		OwnerService: owner, CallerScope: "caller-a", RouteScope: "POST /api/v1/subnet",
		OperationType: "create_subnet", TargetScope: target, IdempotencyKey: uuid.NewString(),
		Payload: map[string]any{"subnet_name": "subnet-a"}, ResourceType: "subnet", ResourceID: uuid.NewString(),
	})
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin create: decision=%s err=%v", decision, err)
	}
	if err := service.MarkTargetRetiring(t.Context(), owner, "subnet", target); err != nil {
		t.Fatalf("retire in-flight target: %v", err)
	}
	persisted, err := service.Get(t.Context(), op.OperationID)
	if err != nil {
		t.Fatalf("load cancelled create: %v", err)
	}
	if persisted.Status != StatusCancelled || persisted.ErrorCode != "DELETE_WON_RACE" {
		t.Fatalf("cancelled create = %#v", persisted)
	}
	if err := service.ReleaseTarget(t.Context(), owner, "subnet", target); err != nil {
		t.Fatalf("release cancelled target: %v", err)
	}
}

func TestRepositoryBeginConcurrentReplayHasOneWinner(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-concurrent-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	cmd := beginTestCommand(t, owner, "key-concurrent", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})

	const contenders = 100
	start := make(chan struct{})
	type result struct {
		op       *Operation
		decision Decision
		err      error
	}
	results := make(chan result, contenders)
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			op, decision, err := repo.Begin(context.Background(), cmd)
			results <- result{op: op, decision: decision, err: err}
		}()
	}
	ready.Wait()
	close(start)
	wg.Wait()
	close(results)

	var operationID string
	newCount := 0
	replayCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent Begin: %v", result.err)
		}
		if operationID == "" {
			operationID = result.op.OperationID
		}
		if result.op.OperationID != operationID {
			t.Fatalf("operation ID = %s, want %s", result.op.OperationID, operationID)
		}
		switch result.decision {
		case DecisionNew:
			newCount++
		case DecisionReplay:
			replayCount++
		default:
			t.Fatalf("unexpected decision: %q", result.decision)
		}
	}
	if newCount != 1 || replayCount != contenders-1 {
		t.Fatalf("new/replay counts = %d/%d, want 1/%d", newCount, replayCount, contenders-1)
	}
	if got := countTestOperations(t, db, owner); got != 1 {
		t.Fatalf("operation rows = %d, want 1", got)
	}
}

func TestServiceClaimDispatchHasOneConcurrentOwner(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-claim-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })

	op, decision, err := service.Begin(context.Background(), BeginRequest{
		OwnerService:   owner,
		CallerScope:    "northbound:test",
		RouteScope:     "POST /api/v1/vpc",
		OperationType:  "create_vpc",
		TargetScope:    "region-a/vpc-a",
		IdempotencyKey: "claim-key",
		Payload:        map[string]any{"vpc_name": "vpc-a"},
		ResourceType:   "vpc",
		ResourceID:     uuid.NewString(),
	})
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin operation: decision=%q err=%v", decision, err)
	}

	const contenders = 100
	start := make(chan struct{})
	type claimResult struct {
		owner   string
		claimed bool
	}
	results := make(chan claimResult, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			leaseOwner, claimed, claimErr := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute)
			if claimErr != nil {
				t.Errorf("claim dispatch: %v", claimErr)
			}
			results <- claimResult{owner: leaseOwner, claimed: claimed}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	owners := 0
	var leaseOwner string
	for result := range results {
		if result.claimed {
			owners++
			leaseOwner = result.owner
		}
	}
	if owners != 1 {
		t.Fatalf("dispatch owners = %d, want 1", owners)
	}
	leaseCtx := ContextWithDispatchLease(context.Background(), op.OperationID, leaseOwner)
	stored, err := service.StoreResponse(leaseCtx, op.OperationID, StatusRunning, "0", map[string]any{
		"code":         "0",
		"operation_id": op.OperationID,
		"status":       StatusRunning,
	})
	if err != nil || !stored {
		t.Fatalf("store dispatch response: stored=%v err=%v", stored, err)
	}
	loaded, err := service.Get(context.Background(), op.OperationID)
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if loaded.Status != StatusRunning || loaded.ResponseCode != "0" || len(loaded.ResponsePayload) == 0 {
		t.Fatalf("loaded operation = %#v", loaded)
	}
}

func TestServiceAcquireDispatchTakesOverExpiredLease(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-lease-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := service.Begin(context.Background(), BeginRequest{
		OwnerService: owner, CallerScope: "test", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc",
		TargetScope: "region-a/vpc-lease", IdempotencyKey: "lease-key", Payload: map[string]any{"vpc_name": "vpc-lease"}, ResourceType: "vpc",
	})
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}
	firstOwner, acquired, err := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute)
	if err != nil || !acquired || firstOwner == "" {
		t.Fatalf("first acquire owner/acquired/err = %q/%v/%v", firstOwner, acquired, err)
	}
	if _, acquired, err := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute); err != nil || acquired {
		t.Fatalf("live lease reacquire = %v/%v", acquired, err)
	}
	if recoverable, err := service.ListRecoverableDispatch(context.Background(), owner, 10); err != nil || len(recoverable) != 0 {
		t.Fatalf("live lease recoverable operations = %d/%v", len(recoverable), err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET lease_until = NOW() - interval '1 second' WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if recoverable, err := service.ListRecoverableDispatch(context.Background(), owner, 10); err != nil || len(recoverable) != 1 || recoverable[0].OperationID != op.OperationID {
		t.Fatalf("expired lease recoverable operations = %#v/%v", recoverable, err)
	}
	secondOwner, acquired, err := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute)
	if err != nil || !acquired || secondOwner == "" || secondOwner == firstOwner {
		t.Fatalf("takeover owner/acquired/err = %q/%v/%v", secondOwner, acquired, err)
	}
}

func TestServiceDispatchLeaseFencesExpiredOwner(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-fence-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := service.Begin(context.Background(), BeginRequest{
		OwnerService: owner, CallerScope: "test", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc",
		TargetScope: "region-a/vpc-fence", IdempotencyKey: "fence-key", Payload: map[string]any{"vpc_name": "vpc-fence"}, ResourceType: "vpc",
	})
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}
	firstOwner, acquired, err := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first acquire: owner=%q acquired=%v err=%v", firstOwner, acquired, err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET lease_until = NOW() - interval '1 second' WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("expire first lease: %v", err)
	}
	secondOwner, acquired, err := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("second acquire: owner=%q acquired=%v err=%v", secondOwner, acquired, err)
	}
	firstCtx := ContextWithDispatchLease(context.Background(), op.OperationID, firstOwner)
	if stored, err := service.StoreResponse(firstCtx, op.OperationID, StatusRunning, "0", map[string]any{"owner": "first"}); err != nil || stored {
		t.Fatalf("expired owner stored response: stored=%v err=%v", stored, err)
	}
	secondCtx := ContextWithDispatchLease(context.Background(), op.OperationID, secondOwner)
	if stored, err := service.StoreResponse(secondCtx, op.OperationID, StatusRunning, "0", map[string]any{"owner": "second"}); err != nil || !stored {
		t.Fatalf("current owner failed to store response: stored=%v err=%v", stored, err)
	}
}

func TestServiceClaimDispatchRenewsLeaseUntilClosed(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-renew-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := service.Begin(context.Background(), BeginRequest{
		OwnerService: owner, CallerScope: "test", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc",
		TargetScope: "region-a/vpc-renew", IdempotencyKey: "renew-key", Payload: map[string]any{"vpc_name": "vpc-renew"}, ResourceType: "vpc",
	})
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}
	lease, acquired, err := service.ClaimDispatch(context.Background(), op.OperationID, 90*time.Millisecond)
	if err != nil || !acquired {
		t.Fatalf("claim renewable lease: lease=%v acquired=%v err=%v", lease, acquired, err)
	}
	t.Cleanup(lease.Close)
	time.Sleep(220 * time.Millisecond)
	if _, acquired, err := service.AcquireDispatch(context.Background(), op.OperationID, time.Minute); err != nil || acquired {
		t.Fatalf("renewed lease was taken over: acquired=%v err=%v", acquired, err)
	}
}

func TestServiceDispatchLeaseLossCancelsBoundContext(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-lease-cancel-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := service.Begin(context.Background(), BeginRequest{
		OwnerService: owner, CallerScope: "test", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc",
		TargetScope: "region-a/vpc-cancel", IdempotencyKey: "cancel-key", Payload: map[string]any{"vpc_name": "vpc-cancel"}, ResourceType: "vpc",
	})
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}
	lease, acquired, err := service.ClaimDispatch(context.Background(), op.OperationID, 90*time.Millisecond)
	if err != nil || !acquired {
		t.Fatalf("claim lease: acquired=%v err=%v", acquired, err)
	}
	t.Cleanup(lease.Close)
	bound := lease.Context(context.Background())
	if _, err := db.Exec(`UPDATE orchestration_operations SET lease_owner = $1 WHERE operation_id = $2`, uuid.NewString(), op.OperationID); err != nil {
		t.Fatalf("replace lease owner: %v", err)
	}
	select {
	case <-bound.Done():
	case <-time.After(time.Second):
		t.Fatal("bound dispatch context was not cancelled after ownership loss")
	}
}

func TestServiceBeginTargetAliasesDifferentKeysForSameSpecification(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-target-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })

	request := BeginRequest{OwnerService: owner, CallerScope: "caller", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc", TargetScope: "region-a/vpc-a", Payload: map[string]any{"vpc_name": "vpc-a"}, ResourceType: "vpc", ResourceID: uuid.NewString()}
	request.IdempotencyKey = "key-a"
	first, firstDecision, err := service.BeginTarget(context.Background(), request)
	if err != nil {
		t.Fatalf("begin first: %v", err)
	}
	request.IdempotencyKey = "key-b"
	request.ResourceID = uuid.NewString()
	second, secondDecision, err := service.BeginTarget(context.Background(), request)
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	if firstDecision != DecisionNew || secondDecision != DecisionReplay || first.OperationID != second.OperationID || first.ResourceID != second.ResourceID {
		t.Fatalf("target operations = %s/%s resources=%s/%s decisions=%s/%s", first.OperationID, second.OperationID, first.ResourceID, second.ResourceID, firstDecision, secondDecision)
	}
	if got := countTestOperations(t, db, owner); got != 1 {
		t.Fatalf("operation rows = %d, want 1", got)
	}
}

func TestServiceBeginTargetPersistsDifferentSpecificationConflict(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-target-conflict-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	base := BeginRequest{OwnerService: owner, CallerScope: "caller", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc", TargetScope: "region-a/vpc-a", IdempotencyKey: "key-a", Payload: map[string]any{"vpc_name": "vpc-a", "vlan_id": 101}, ResourceType: "vpc", ResourceID: uuid.NewString()}
	first, decision, err := service.BeginTarget(context.Background(), base)
	if err != nil || decision != DecisionNew {
		t.Fatalf("first target operation: %#v/%s/%v", first, decision, err)
	}
	base.IdempotencyKey = "key-b"
	base.ResourceID = uuid.NewString()
	base.Payload = map[string]any{"vpc_name": "vpc-a", "vlan_id": 202}
	conflict, decision, err := service.BeginTarget(context.Background(), base)
	if err != nil || decision != DecisionResourceConflict {
		t.Fatalf("conflicting target operation: %#v/%s/%v", conflict, decision, err)
	}
	if conflict.Status != StatusFailed || conflict.ResourceID != first.ResourceID || conflict.ErrorCode != ErrResourceSpecConflict.Error() {
		t.Fatalf("persisted conflict = %#v", conflict)
	}
	replayed, replayDecision, err := service.BeginTarget(context.Background(), base)
	if err != nil || replayDecision != DecisionResourceConflict || replayed.OperationID != conflict.OperationID || replayed.Status != StatusFailed {
		t.Fatalf("replayed conflict = %#v/%s/%v", replayed, replayDecision, err)
	}
}

func TestServiceFailedTargetCanStartNewGenerationWithNewKey(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-target-generation-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	request := BeginRequest{OwnerService: owner, CallerScope: "caller", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc", TargetScope: "region-a/vpc-a", IdempotencyKey: "generation-key-1", Payload: map[string]any{"vpc_name": "vpc-a"}, ResourceType: "vpc", ResourceID: uuid.NewString()}
	first, decision, err := service.BeginTarget(context.Background(), request)
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin first generation: op=%#v decision=%s err=%v", first, decision, err)
	}
	lease, acquired, err := service.ClaimDispatch(context.Background(), first.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("claim first generation: acquired=%v err=%v", acquired, err)
	}
	if stored, err := service.StoreResponseAndReleaseTarget(lease.Context(context.Background()), first.OperationID, StatusFailed, "FAILED", map[string]any{"code": "FAILED"}); err != nil || !stored {
		t.Fatalf("fail first generation: stored=%v err=%v", stored, err)
	}
	lease.Close()

	request.IdempotencyKey = "generation-key-2"
	request.ResourceID = uuid.NewString()
	second, decision, err := service.BeginTarget(context.Background(), request)
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin second generation: op=%#v decision=%s err=%v", second, decision, err)
	}
	if second.Generation != first.Generation+1 || second.ResourceID == first.ResourceID {
		t.Fatalf("second identity = resource:%s generation:%d, first=%s/%d", second.ResourceID, second.Generation, first.ResourceID, first.Generation)
	}
}

func TestServiceFailedTargetRemainsClaimedWithoutExplicitRelease(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "operation-target-retained-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	request := BeginRequest{OwnerService: owner, CallerScope: "caller", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc", TargetScope: "region-a/vpc-a", IdempotencyKey: "retained-key-1", Payload: map[string]any{"vpc_name": "vpc-a"}, ResourceType: "vpc", ResourceID: uuid.NewString()}
	first, _, err := service.BeginTarget(context.Background(), request)
	if err != nil {
		t.Fatalf("begin first operation: %v", err)
	}
	lease, acquired, err := service.ClaimDispatch(context.Background(), first.OperationID, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("claim first operation: acquired=%v err=%v", acquired, err)
	}
	if stored, err := service.StoreResponse(lease.Context(context.Background()), first.OperationID, StatusFailed, "FAILED", map[string]any{"code": "FAILED"}); err != nil || !stored {
		t.Fatalf("store retained failure: stored=%v err=%v", stored, err)
	}
	lease.Close()
	request.IdempotencyKey = "retained-key-2"
	request.ResourceID = uuid.NewString()
	replayed, decision, err := service.BeginTarget(context.Background(), request)
	if err != nil || decision != DecisionReplay || replayed.OperationID != first.OperationID {
		t.Fatalf("retained target replay = %#v/%s/%v", replayed, decision, err)
	}
}

func TestReleaseTargetRejectsActiveCreateAndAllowsTerminalCreate(t *testing.T) {
	db := openOperationTestDB(t)
	service := NewService(NewRepository(db))
	owner := "delete-guard-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	request := BeginRequest{OwnerService: owner, CallerScope: "caller", RouteScope: "POST /api/v1/vpc", OperationType: "create_vpc", TargetScope: "vpc-a", IdempotencyKey: "delete-guard-key", Payload: map[string]any{"vpc_name": "vpc-a"}, ResourceType: "vpc", ResourceID: uuid.NewString()}
	op, decision, err := service.BeginTarget(t.Context(), request)
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin create: %#v/%s/%v", op, decision, err)
	}
	if err := service.AssertTargetReleasable(t.Context(), owner, "vpc", request.TargetScope); !errors.Is(err, ErrResourceOperationInProgress) {
		t.Fatalf("assert active target releasable = %v", err)
	}
	if err := service.ReleaseTarget(t.Context(), owner, "vpc", request.TargetScope); !errors.Is(err, ErrResourceOperationInProgress) {
		t.Fatalf("release active target = %v", err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET status = 'succeeded', completed_at = NOW() WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("complete create: %v", err)
	}
	if err := service.ReleaseTarget(t.Context(), owner, "vpc", request.TargetScope); err != nil {
		t.Fatalf("release terminal target: %v", err)
	}
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_target_claims WHERE operation_id = $1 AND active = TRUE`, op.OperationID).Scan(&active); err != nil {
		t.Fatalf("count active claims: %v", err)
	}
	if active != 0 {
		t.Fatalf("active target claims = %d, want 0", active)
	}
}

func TestRepositoryDeferredRecoveryDoesNotStarveLaterOperations(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-recovery-fairness-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	ids := make([]string, 0, 101)
	for index := 0; index < 101; index++ {
		cmd := beginTestCommand(t, owner, fmt.Sprintf("fair-key-%03d", index), fmt.Sprintf("region/vpc-%03d", index), map[string]any{"index": index})
		op, decision, err := repo.Begin(context.Background(), cmd)
		if err != nil || decision != DecisionNew {
			t.Fatalf("begin operation %d: decision=%s err=%v", index, decision, err)
		}
		ids = append(ids, op.OperationID)
	}
	firstPage, err := repo.ListRecoverableDispatch(context.Background(), owner, 100)
	if err != nil || len(firstPage) != 100 {
		t.Fatalf("first recovery page: count=%d err=%v", len(firstPage), err)
	}
	for _, op := range firstPage {
		if err := repo.DeferDispatch(context.Background(), op.OperationID); err != nil {
			t.Fatalf("defer operation %s: %v", op.OperationID, err)
		}
	}
	secondPage, err := repo.ListRecoverableDispatch(context.Background(), owner, 100)
	if err != nil || len(secondPage) == 0 {
		t.Fatalf("second recovery page: count=%d err=%v", len(secondPage), err)
	}
	found := false
	for _, op := range secondPage {
		if op.OperationID == ids[100] {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("later operation %s remained starved", ids[100])
	}
}

func TestRepositoryDeferredRunningReconcileDoesNotStarveLaterOperations(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-running-fairness-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	ids := make([]string, 0, 201)
	for index := 0; index < 201; index++ {
		op, decision, err := repo.Begin(context.Background(), beginTestCommand(t, owner, fmt.Sprintf("running-key-%03d", index), fmt.Sprintf("region/running-%03d", index), map[string]any{"index": index}))
		if err != nil || decision != DecisionNew {
			t.Fatalf("begin running operation %d: decision=%s err=%v", index, decision, err)
		}
		if changed, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, op.Version, op.OperationType, StatusAccepted, StatusDispatching, "", ""); err != nil || !changed {
			t.Fatalf("dispatch operation %d: changed=%v err=%v", index, changed, err)
		}
		if changed, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, op.Version+1, op.OperationType, StatusDispatching, StatusRunning, "", ""); err != nil || !changed {
			t.Fatalf("run operation %d: changed=%v err=%v", index, changed, err)
		}
		ids = append(ids, op.OperationID)
	}
	firstPage, err := repo.ListByStatus(context.Background(), owner, StatusRunning, 200)
	if err != nil || len(firstPage) != 200 {
		t.Fatalf("first running page: count=%d err=%v", len(firstPage), err)
	}
	for _, op := range firstPage {
		if err := repo.DeferStatus(context.Background(), op.OperationID, StatusRunning); err != nil {
			t.Fatalf("defer running operation %s: %v", op.OperationID, err)
		}
	}
	secondPage, err := repo.ListByStatus(context.Background(), owner, StatusRunning, 200)
	if err != nil || len(secondPage) == 0 {
		t.Fatalf("second running page: count=%d err=%v", len(secondPage), err)
	}
	found := false
	for _, op := range secondPage {
		if op.OperationID == ids[200] {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("later running operation %s remained starved", ids[200])
	}
}

func TestOperationMigrationBackfillsLegacyTopResourceClaim(t *testing.T) {
	db := openOperationTestDB(t)
	unique := uuid.NewString()
	vpcID := "legacy-resource-" + unique
	vpcName := "legacy-vpc-" + unique
	region := "legacy-region-" + unique
	target := vpcName
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, target)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, target)
		_, _ = db.Exec(`DELETE FROM vpc_registry WHERE id = $1`, vpcID)
	})
	if _, err := db.Exec(`INSERT INTO vpc_registry (id, vpc_name, region, status) VALUES ($1, $2, $3, 'running')`, vpcID, vpcName, region); err != nil {
		t.Fatalf("insert legacy VPC: %v", err)
	}
	migration, err := os.ReadFile("../db/migrations/005_create_operations.sql")
	if err != nil {
		t.Fatalf("read operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("reapply operation migration: %v", err)
	}
	var claimedResource string
	if err := db.QueryRow(`
		SELECT resource_id FROM orchestration_target_claims
		WHERE owner_service = 'top-nsp-vpc' AND resource_type = 'vpc' AND target_scope = $1 AND active = TRUE
	`, target).Scan(&claimedResource); err != nil {
		t.Fatalf("load backfilled claim: %v", err)
	}
	if claimedResource != vpcID {
		t.Fatalf("backfilled resource = %s, want %s", claimedResource, vpcID)
	}
}

func TestOperationMigrationDoesNotReactivateDeletedTopResource(t *testing.T) {
	db := openOperationTestDB(t)
	unique := uuid.NewString()
	vpcID := "deleted-resource-" + unique
	vpcName := "deleted-vpc-" + unique
	region := "deleted-region-" + unique
	target := vpcName
	key := "deleted-key-" + unique
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, target)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, target)
		_, _ = db.Exec(`DELETE FROM vpc_registry WHERE id = $1`, vpcID)
	})
	if _, err := db.Exec(`INSERT INTO vpc_registry (id, vpc_name, region, status) VALUES ($1, $2, $3, 'deleted')`, vpcID, vpcName, region); err != nil {
		t.Fatalf("insert deleted VPC: %v", err)
	}
	cmd := beginTestCommand(t, "top-nsp-vpc", key, target, map[string]any{"vpc_name": vpcName, "region": region})
	cmd.ResourceID = vpcID
	op, decision, err := NewRepository(db).Begin(context.Background(), cmd)
	if err != nil || decision != DecisionNew {
		t.Fatalf("insert historic create operation: op=%#v decision=%s err=%v", op, decision, err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET status = 'succeeded' WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("mark historic operation succeeded: %v", err)
	}
	migration, err := os.ReadFile("../db/migrations/005_create_operations.sql")
	if err != nil {
		t.Fatalf("read operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("reapply operation migration: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1 AND active = TRUE`, target).Scan(&count); err != nil {
		t.Fatalf("count deleted target claims: %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted target was reactivated with %d active claims", count)
	}
}

func TestRepositoryStoresReplayableResponseWithVersionCAS(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-response-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	cmd := beginTestCommand(t, owner, "key-response", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})

	op, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil || decision != DecisionNew {
		t.Fatalf("begin operation: decision=%q err=%v", decision, err)
	}
	updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "create_vpc", StatusAccepted, StatusDispatching, "", "")
	if err != nil || !updated {
		t.Fatalf("dispatch operation: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 1, "create_vpc", StatusDispatching, StatusRunning, "", "")
	if err != nil || !updated {
		t.Fatalf("run operation: updated=%v err=%v", updated, err)
	}
	response := json.RawMessage(`{"code":"0","operation_id":"` + op.OperationID + `","status":"succeeded"}`)
	updated, err = repo.StoreResponseCAS(context.Background(), op.OperationID, 2, "create_vpc", StatusRunning, StatusSucceeded, "0", response)
	if err != nil || !updated {
		t.Fatalf("store response: updated=%v err=%v", updated, err)
	}
	updated, err = repo.StoreResponseCAS(context.Background(), op.OperationID, 2, "create_vpc", StatusRunning, StatusFailed, "FAILED", json.RawMessage(`{"code":"FAILED"}`))
	if err != nil {
		t.Fatalf("stale store response: %v", err)
	}
	if updated {
		t.Fatal("stale version overwrote stored response")
	}

	replayed, decision, err := repo.Begin(context.Background(), cmd)
	if err != nil || decision != DecisionReplay {
		t.Fatalf("replay operation: decision=%q err=%v", decision, err)
	}
	if replayed.Status != StatusSucceeded || replayed.ResponseCode != "0" {
		t.Fatalf("replayed response = status:%q code:%q payload:%s", replayed.Status, replayed.ResponseCode, replayed.ResponsePayload)
	}
	assertJSONEqual(t, replayed.ResponsePayload, response)
}

func TestRepositoryStatusCASCannotRegressTerminalState(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-status-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-status", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}

	updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "create_vpc", StatusAccepted, StatusDispatching, "", "")
	if err != nil || !updated {
		t.Fatalf("accepted -> dispatching: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 1, "create_vpc", StatusDispatching, StatusRunning, "", "")
	if err != nil || !updated {
		t.Fatalf("dispatching -> running: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 2, "create_vpc", StatusRunning, StatusSucceeded, "", "")
	if err != nil || !updated {
		t.Fatalf("running -> succeeded: updated=%v err=%v", updated, err)
	}
	updated, err = repo.UpdateStatusCAS(context.Background(), op.OperationID, 3, "create_vpc", StatusSucceeded, StatusRunning, "", "")
	if err == nil || updated {
		t.Fatalf("terminal regression updated=%v err=%v", updated, err)
	}
}

func TestRepositoryStatusCASRejectsBackwardUnknownAndArbitraryResponseTransitions(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-invalid-status-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-invalid-status", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}

	for _, test := range []struct {
		name     string
		expected Status
		next     Status
	}{
		{name: "skips dispatching", expected: StatusAccepted, next: StatusRunning},
		{name: "unknown next", expected: StatusAccepted, next: Status("unknown")},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "create_vpc", test.expected, test.next, "", "")
			if err == nil || updated {
				t.Fatalf("invalid transition updated=%v err=%v", updated, err)
			}
		})
	}

	updated, err := repo.StoreResponseCAS(context.Background(), op.OperationID, 0, "create_vpc", StatusAccepted, StatusRunning, "0", json.RawMessage(`{"code":"0"}`))
	if err == nil || updated {
		t.Fatalf("arbitrary response transition updated=%v err=%v", updated, err)
	}
}

func TestRepositoryStatusCASMatchesPersistedOperationType(t *testing.T) {
	db := openOperationTestDB(t)
	repo := NewRepository(db)
	owner := "operation-type-status-" + uuid.NewString()
	t.Cleanup(func() { deleteTestOperations(t, db, owner) })
	op, _, err := repo.Begin(context.Background(), beginTestCommand(t, owner, "key-operation-type", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"}))
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}

	updated, err := repo.UpdateStatusCAS(context.Background(), op.OperationID, 0, "delete_vpc", StatusAccepted, StatusDeleted, "", "")
	if err != nil {
		t.Fatalf("mismatched operation type: %v", err)
	}
	if updated {
		t.Fatal("delete state machine updated a persisted create operation")
	}
}

func TestValidateBeginCommandRejectsNonHexadecimalHash(t *testing.T) {
	cmd := BeginCommand{
		OwnerService:       "owner",
		CallerScope:        "caller",
		RouteScope:         "POST /api/v1/vpc",
		OperationType:      "create_vpc",
		TargetScope:        "region-a/vpc-a",
		IdempotencyKey:     "key",
		RequestHashVersion: CanonicalHashVersion,
		RequestHash:        "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		RequestPayload:     json.RawMessage(`{"vpc_name":"vpc-a"}`),
		ResourceType:       "vpc",
	}
	if err := validateBeginCommand(cmd); err == nil {
		t.Fatal("non-hexadecimal request hash accepted")
	}
}

func TestValidateBeginCommandRejectsUnknownOperationType(t *testing.T) {
	cmd := beginTestCommand(t, "owner", "key", "region-a/vpc-a", map[string]any{"vpc_name": "vpc-a"})
	cmd.OperationType = "crate_vpc"
	if err := validateBeginCommand(cmd); err == nil {
		t.Fatal("unknown operation type accepted")
	}
}

func TestStoreResponseCASRejectsUnknownOperationTypeForSameStatus(t *testing.T) {
	repo := NewRepository(nil)
	updated, err := repo.StoreResponseCAS(
		context.Background(),
		"operation-id",
		0,
		"crate_vpc",
		StatusAccepted,
		StatusAccepted,
		"0",
		json.RawMessage(`{"code":"0"}`),
	)
	if err == nil || updated {
		t.Fatalf("unknown operation type same-status response updated=%v err=%v", updated, err)
	}
}

func beginTestCommand(t *testing.T, owner, key, target string, payload any) BeginCommand {
	t.Helper()
	hash, canonical, err := CanonicalHash(CanonicalHashVersion, target, payload)
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	return BeginCommand{
		OwnerService:       owner,
		CallerScope:        "caller-a",
		RouteScope:         "POST /api/v1/vpc",
		OperationType:      "create_vpc",
		TargetScope:        target,
		IdempotencyKey:     key,
		RequestHashVersion: CanonicalHashVersion,
		RequestHash:        hash,
		RequestPayload:     canonical,
		ResourceType:       "vpc",
		Generation:         1,
	}
}

func openOperationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	migration, err := os.ReadFile("../db/migrations/005_create_operations.sql")
	if err != nil {
		t.Fatalf("read operation migration: %v", err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatalf("apply operation migration: %v", err)
	}
	return db
}

func countTestOperations(t *testing.T, db *sql.DB, owner string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service = $1`, owner).Scan(&count); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	return count
}

func deleteTestOperations(t *testing.T, db *sql.DB, owner string) {
	t.Helper()
	if _, err := db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = $1`, owner); err != nil {
		t.Errorf("delete target claims: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = $1`, owner); err != nil {
		t.Errorf("delete operations: %v", err)
	}
}

func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode got JSON: %v; payload=%s", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("decode want JSON: %v; payload=%s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch: got=%s want=%s", got, want)
	}
}
