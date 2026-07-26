package dao

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func TestDeletePCCNAndReleaseTargetCommitsAtomically(t *testing.T) {
	db := openPCCNDAOTestDB(t)
	unique := uuid.NewString()
	pccnID := uuid.NewString()
	pccnName := "atomic-pccn-" + unique
	target := pccnName
	opService := operation.NewService(operation.NewRepository(db))
	op, decision, err := opService.BeginTarget(t.Context(), operation.BeginRequest{
		OwnerService: "top-nsp-vpc", CallerScope: "test", RouteScope: "POST /api/v1/pccn",
		OperationType: "create_pccn", TargetScope: target, IdempotencyKey: "atomic-pccn-key-" + unique,
		Payload: map[string]any{"pccn_name": pccnName}, ResourceType: "pccn", ResourceID: pccnID,
	})
	if err != nil || decision != operation.DecisionNew {
		t.Fatalf("claim PCCN target: op=%#v decision=%s err=%v", op, decision, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM pccn_registry WHERE id = $1`, pccnID)
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vpc' AND target_scope = $1`, target)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE operation_id = $1`, op.OperationID)
	})
	dao := NewTopPCCNDAO(db)
	if err := dao.RegisterPCCN(t.Context(), &models.PCCNRegistry{ID: pccnID, PCCNName: pccnName, VPC1Name: "vpc-a", VPC1Region: "region-a", VPC2Name: "vpc-b", VPC2Region: "region-b", Status: "running", VPCDetails: map[string]models.VPCDetail{}}); err != nil {
		t.Fatalf("register PCCN: %v", err)
	}
	if err := dao.DeletePCCNAndReleaseTarget(t.Context(), pccnName); !errors.Is(err, operation.ErrResourceOperationInProgress) {
		t.Fatalf("delete while create is active = %v, want operation-in-progress", err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET status = 'succeeded', completed_at = NOW() WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("complete create operation: %v", err)
	}
	if err := dao.DeletePCCNAndReleaseTarget(t.Context(), pccnName); err != nil {
		t.Fatalf("delete PCCN atomically: %v", err)
	}
	var resources, activeClaims int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pccn_registry WHERE id = $1`, pccnID).Scan(&resources); err != nil {
		t.Fatalf("count PCCN: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_target_claims WHERE operation_id = $1 AND active = TRUE`, op.OperationID).Scan(&activeClaims); err != nil {
		t.Fatalf("count active claims: %v", err)
	}
	if resources != 0 || activeClaims != 0 {
		t.Fatalf("PCCN/active claims after delete = %d/%d", resources, activeClaims)
	}
}

func TestUpdatePCCNStatusWithoutDetailsPreservesTargetSnapshot(t *testing.T) {
	db := openPCCNDAOTestDB(t)
	pccnID := uuid.NewString()
	pccnName := "snapshot-pccn-" + uuid.NewString()
	dao := NewTopPCCNDAO(db)
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM pccn_registry WHERE id = $1`, pccnID)
	})
	wantDetails := map[string]models.VPCDetail{
		"region-a/vpc-a": {Region: "region-a", AZs: []string{"az-a"}, Status: "creating"},
	}
	if err := dao.RegisterPCCN(t.Context(), &models.PCCNRegistry{
		ID: pccnID, PCCNName: pccnName,
		VPC1Name: "vpc-a", VPC1Region: "region-a",
		VPC2Name: "vpc-b", VPC2Region: "region-b",
		Status: "creating", VPCDetails: wantDetails,
	}); err != nil {
		t.Fatalf("register PCCN: %v", err)
	}
	if err := dao.UpdatePCCNStatus(t.Context(), pccnName, "failed", nil); err != nil {
		t.Fatalf("update PCCN status: %v", err)
	}
	got, err := dao.GetPCCNByName(t.Context(), pccnName)
	if err != nil {
		t.Fatalf("get PCCN: %v", err)
	}
	detail := got.VPCDetails["region-a/vpc-a"]
	if got.Status != "failed" || len(detail.AZs) != 1 || detail.AZs[0] != "az-a" {
		t.Fatalf("status/details after update = %s/%#v, want failed with preserved AZ", got.Status, detail)
	}
}

func openPCCNDAOTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("NSP_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("NSP_TEST_POSTGRES_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}
	return db
}
