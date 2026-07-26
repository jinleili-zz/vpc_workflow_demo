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

func TestDeletePolicyAndReleaseTargetCommitsAtomically(t *testing.T) {
	db := openVFWDAOTestDB(t)
	unique := uuid.NewString()
	policyID := uuid.NewString()
	policyName := "atomic-policy-" + unique
	opService := operation.NewService(operation.NewRepository(db))
	op, decision, err := opService.BeginTarget(t.Context(), operation.BeginRequest{
		OwnerService: "top-nsp-vfw", CallerScope: "test", RouteScope: "POST /api/v1/firewall/policy",
		OperationType: "apply_firewall_policy", TargetScope: policyName, IdempotencyKey: "atomic-policy-key-" + unique,
		Payload: map[string]any{"policy_name": policyName}, ResourceType: "firewall_policy", ResourceID: policyID,
	})
	if err != nil || decision != operation.DecisionNew {
		t.Fatalf("claim policy target: op=%#v decision=%s err=%v", op, decision, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM policy_az_records WHERE policy_id = $1`, policyID)
		_, _ = db.Exec(`DELETE FROM policy_registry WHERE id = $1`, policyID)
		_, _ = db.Exec(`DELETE FROM orchestration_target_claims WHERE owner_service = 'top-nsp-vfw' AND target_scope = $1`, policyName)
		_, _ = db.Exec(`DELETE FROM orchestration_operations WHERE operation_id = $1`, op.OperationID)
	})
	dao := NewTopVFWDAO(db)
	if err := dao.CreatePolicy(t.Context(), &models.PolicyRegistry{ID: policyID, PolicyName: policyName, SourceIP: "10.0.0.1", DestIP: "10.0.0.2", Protocol: "tcp", Action: "allow", Status: "running"}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	if err := dao.CreateAZRecord(t.Context(), &models.PolicyAZRecord{ID: uuid.NewString(), PolicyID: policyID, Region: "region-a", AZ: "az-a", Status: "running"}); err != nil {
		t.Fatalf("create policy AZ record: %v", err)
	}
	if err := dao.DeletePolicyAndReleaseTarget(t.Context(), policyID); !errors.Is(err, operation.ErrResourceOperationInProgress) {
		t.Fatalf("delete while create is active = %v, want operation-in-progress", err)
	}
	if _, err := db.Exec(`UPDATE orchestration_operations SET status = 'succeeded', completed_at = NOW() WHERE operation_id = $1`, op.OperationID); err != nil {
		t.Fatalf("complete create operation: %v", err)
	}
	if err := dao.DeletePolicyAndReleaseTarget(t.Context(), policyID); err != nil {
		t.Fatalf("delete policy atomically: %v", err)
	}
	var policies, activeClaims int
	if err := db.QueryRow(`SELECT COUNT(*) FROM policy_registry WHERE id = $1`, policyID).Scan(&policies); err != nil {
		t.Fatalf("count policy: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM orchestration_target_claims WHERE operation_id = $1 AND active = TRUE`, op.OperationID).Scan(&activeClaims); err != nil {
		t.Fatalf("count active claims: %v", err)
	}
	if policies != 0 || activeClaims != 0 {
		t.Fatalf("policy/active claims after delete = %d/%d", policies, activeClaims)
	}
}

func TestPolicyAZRecordsUseRegionAndAZIdentity(t *testing.T) {
	db := openVFWDAOTestDB(t)
	unique := uuid.NewString()
	policyID := uuid.NewString()
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM policy_registry WHERE id = $1`, policyID) })
	dao := NewTopVFWDAO(db)
	if err := dao.CreatePolicy(t.Context(), &models.PolicyRegistry{ID: policyID, PolicyName: "region-az-policy-" + unique, SourceIP: "10.1.0.1", DestIP: "10.2.0.1", Protocol: "tcp", Action: "allow", Status: "creating"}); err != nil {
		t.Fatalf("create policy: %v", err)
	}
	for _, region := range []string{"region-a", "region-b"} {
		if err := dao.CreateAZRecord(t.Context(), &models.PolicyAZRecord{ID: uuid.NewString(), PolicyID: policyID, Region: region, AZ: "az-shared", Status: "creating"}); err != nil {
			t.Fatalf("create %s AZ record: %v", region, err)
		}
	}
	records, err := dao.GetAZRecords(t.Context(), policyID)
	if err != nil {
		t.Fatalf("get AZ records: %v", err)
	}
	if len(records) != 2 || records[0].Region == records[1].Region {
		t.Fatalf("region/AZ records = %#v, want distinct regions with shared AZ label", records)
	}
}

func TestCountPoliciesByZoneIncludesPartiallyFailedPolicy(t *testing.T) {
	db := openVFWDAOTestDB(t)
	policyID := uuid.NewString()
	zone := "zone-partial-" + uuid.NewString()
	dao := NewTopVFWDAO(db)
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM policy_registry WHERE id = $1`, policyID) })
	if err := dao.CreatePolicy(t.Context(), &models.PolicyRegistry{
		ID: policyID, PolicyName: "partial-policy-" + uuid.NewString(),
		SourceIP: "10.31.0.1", DestIP: "10.32.0.1", Protocol: "tcp", Action: "allow",
		SourceZone: zone, DestZone: "other-zone", Status: "failed",
	}); err != nil {
		t.Fatalf("create partially failed policy: %v", err)
	}
	count, err := dao.CountPoliciesByZone(t.Context(), zone)
	if err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if count != 1 {
		t.Fatalf("failed policy dependency count = %d, want 1", count)
	}
}

func openVFWDAOTestDB(t *testing.T) *sql.DB {
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
