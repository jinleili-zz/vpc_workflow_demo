package orchestrator

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"workflow_qoder/internal/db/dao"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestDeleteSubnetAtomicallyCancelsCreateAndRetiresAZTarget(t *testing.T) {
	db := openAZDeleteTestDB(t)
	region := "region-delete-" + uuid.NewString()
	az := "az-delete-" + uuid.NewString()
	name := "subnet-delete-" + uuid.NewString()
	owner := "az-nsp-vpc-" + az
	target := region + "/" + az + "/" + name
	resourceID := uuid.NewString()
	service := operation.NewService(operation.NewRepository(db))
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM worker_device_state WHERE target_key = $1`, resourceID)
		_, _ = db.Exec(`DELETE FROM subnet_resources WHERE id = $1`, resourceID)
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = $1`, owner)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = $1`, owner)
	})

	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin create transaction: %v", err)
	}
	op, decision, err := service.BeginTargetTx(t.Context(), tx, operation.BeginRequest{
		OwnerService: owner, CallerScope: "top-nsp-vpc", RouteScope: "POST /api/v1/subnet",
		OperationType: "create_subnet", TargetScope: target, IdempotencyKey: uuid.NewString(),
		Payload:      map[string]any{"subnet_name": name, "cidr": "10.90.0.0/24"},
		ResourceType: "subnet", ResourceID: resourceID, Generation: 1,
	})
	if err != nil || decision != operation.DecisionNew {
		_ = tx.Rollback()
		t.Fatalf("create operation target: decision=%s err=%v", decision, err)
	}
	if err := dao.NewSubnetDAO(db).CreateTx(t.Context(), tx, &models.SubnetResource{
		ID: resourceID, SubnetName: name, VPCName: "vpc-parent", Region: region, AZ: az,
		CIDR: "10.90.0.0/24", Status: models.ResourceStatusRunning,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("create subnet resource: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit create transaction: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO worker_device_state (
			operation_key, device_type, target_key, desired_hash, result_payload
		) VALUES ($1, 'switch', $2, $3, '{}'::jsonb)
	`, "seed-device-"+uuid.NewString(), resourceID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("seed device state: %v", err)
	}

	orch := NewAZOrchestrator(db, nil, nil, nil, region, az)
	if err := orch.DeleteSubnet(t.Context(), name); err != nil {
		t.Fatalf("delete while create is active: %v", err)
	}
	var resources int
	var createStatus operation.Status
	if err := db.QueryRow(`SELECT status FROM orchestration_operations WHERE operation_id = $1`, op.OperationID).Scan(&createStatus); err != nil {
		t.Fatalf("load cancelled create operation: %v", err)
	}
	if createStatus != operation.StatusCancelled {
		t.Fatalf("create status after delete = %s, want cancelled", createStatus)
	}
	if err := orch.DeleteSubnet(t.Context(), name); err != nil {
		t.Fatalf("repeat subnet delete: %v", err)
	}
	var activeClaims int
	var deviceStates int
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_target_claims WHERE owner_service = $1 AND target_scope = $2 AND active = TRUE`, owner, target).Scan(&activeClaims); err != nil {
		t.Fatalf("count active claims: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM subnet_resources WHERE id = $1`, resourceID).Scan(&resources); err != nil {
		t.Fatalf("count deleted resource: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM worker_device_state WHERE target_key = $1`, resourceID).Scan(&deviceStates); err != nil {
		t.Fatalf("count device state: %v", err)
	}
	if resources != 0 || activeClaims != 0 || deviceStates != 0 {
		t.Fatalf("resource/active claims/device state after delete = %d/%d/%d, want 0/0/0", resources, activeClaims, deviceStates)
	}

	next, decision, err := service.BeginTarget(t.Context(), operation.BeginRequest{
		OwnerService: owner, CallerScope: "top-nsp-vpc", RouteScope: "POST /api/v1/subnet",
		OperationType: "create_subnet", TargetScope: target, IdempotencyKey: uuid.NewString(),
		Payload:      map[string]any{"subnet_name": name, "cidr": "10.91.0.0/24"},
		ResourceType: "subnet", ResourceID: uuid.NewString(),
	})
	if err != nil || decision != operation.DecisionNew || next.Generation != 2 {
		t.Fatalf("recreate target: op=%#v decision=%s err=%v, want generation 2", next, decision, err)
	}
}

func openAZDeleteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	return db
}
