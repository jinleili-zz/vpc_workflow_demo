package idempotency

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"workflow_qoder/internal/operation"
)

const svcTestOwner = "idem-svc-test"

func cleanupSvcOps(t *testing.T) {
	t.Helper()
	db := openDB(t, azDBName)
	if _, err := db.Exec(`DELETE FROM orchestration_operations WHERE owner_service = $1`, svcTestOwner); err != nil {
		t.Fatalf("清理测试 Operation 失败: %v", err)
	}
}

type vpcTestRequest struct {
	VPCName string `json:"vpc_name"`
	Region  string `json:"region"`
	VLANId  int    `json:"vlan_id"`
}

func newService(t *testing.T) *operation.Service {
	t.Helper()
	db := openDB(t, azDBName)
	return operation.NewService(db, svcTestOwner)
}

// 相同 Key + 相同请求 -> 同一 Operation；相同 Key + 不同请求 -> ErrRequestConflict。
// 对应设计文档 15.2 节"顺序重复 POST"与"相同 Key 不同 Body"场景的 Service 层验证。
func TestOperationBeginReplayAndConflict(t *testing.T) {
	requirePostgres(t)
	cleanupSvcOps(t)
	svc := newService(t)
	ctx := context.Background()

	key := uniqueName("svc-key")
	cmd := operation.BeginCommand{
		CallerScope:    "northbound",
		RouteScope:     "POST /api/v1/vpc",
		OperationType:  "create_vpc",
		IdempotencyKey: key,
		Request:        vpcTestRequest{VPCName: "vpc-a", Region: "region-test", VLANId: 100},
	}

	op1, created1, err := svc.Begin(ctx, cmd)
	if err != nil || !created1 {
		t.Fatalf("首次 Begin created=%v err=%v, want created=true", created1, err)
	}

	op2, created2, err := svc.Begin(ctx, cmd)
	if err != nil || created2 {
		t.Fatalf("重复 Begin created=%v err=%v, want created=false", created2, err)
	}
	if op2.OperationID != op1.OperationID {
		t.Fatalf("重复 Begin 返回不同 Operation: %s != %s", op2.OperationID, op1.OperationID)
	}

	// 相同 Key、不同请求体 -> 冲突
	cmdConflict := cmd
	cmdConflict.Request = vpcTestRequest{VPCName: "vpc-a", Region: "region-test", VLANId: 200}
	_, _, err = svc.Begin(ctx, cmdConflict)
	if err != operation.ErrRequestConflict {
		t.Fatalf("同 Key 不同请求 err=%v, want ErrRequestConflict", err)
	}

	// 不同 Key、相同请求体 -> 新 Operation（不互相去重）
	cmdNewKey := cmd
	cmdNewKey.IdempotencyKey = uniqueName("svc-key")
	op3, created3, err := svc.Begin(ctx, cmdNewKey)
	if err != nil || !created3 {
		t.Fatalf("新 Key Begin created=%v err=%v, want created=true", created3, err)
	}
	if op3.OperationID == op1.OperationID {
		t.Fatalf("新 Key 不应复用旧 Operation")
	}
}

// 并发 50 个相同 Key 请求，只有 1 个赢得创建权，其余拿到同一 Operation。
// 对应设计文档 15.2 节"并发重复 POST 100 次"场景的 Service 层验证。
func TestOperationBeginConcurrent(t *testing.T) {
	requirePostgres(t)
	cleanupSvcOps(t)
	svc := newService(t)
	ctx := context.Background()

	key := uniqueName("svc-conc")
	cmd := operation.BeginCommand{
		CallerScope:    "northbound",
		RouteScope:     "POST /api/v1/vpc",
		OperationType:  "create_vpc",
		IdempotencyKey: key,
		Request:        vpcTestRequest{VPCName: "vpc-conc", Region: "region-test", VLANId: 100},
	}

	const n = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	createdCount := make(chan bool, n)
	opIDs := make(chan string, n)
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			op, created, err := svc.Begin(ctx, cmd)
			if err != nil {
				errs <- err
				return
			}
			createdCount <- created
			opIDs <- op.OperationID
		}()
	}
	close(start)
	wg.Wait()
	close(createdCount)
	close(opIDs)
	close(errs)

	for err := range errs {
		t.Fatalf("并发 Begin 出错: %v", err)
	}

	winners := 0
	for created := range createdCount {
		if created {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("并发 Begin 胜出数 = %d, want 1", winners)
	}

	var first string
	for id := range opIDs {
		if first == "" {
			first = id
			continue
		}
		if id != first {
			t.Fatalf("并发 Begin 返回多个 Operation: %s 与 %s", first, id)
		}
	}
}

// Complete 后响应可重放；WaitTerminal 能等到终态。
func TestOperationCompleteAndWaitTerminal(t *testing.T) {
	requirePostgres(t)
	cleanupSvcOps(t)
	svc := newService(t)
	ctx := context.Background()

	op, created, err := svc.Begin(ctx, operation.BeginCommand{
		CallerScope:    "northbound",
		RouteScope:     "POST /api/v1/subnet",
		OperationType:  "create_subnet",
		IdempotencyKey: uniqueName("svc-wait"),
		Request:        vpcTestRequest{VPCName: "vpc-w", Region: "region-test", VLANId: 1},
	})
	if err != nil || !created {
		t.Fatalf("Begin created=%v err=%v", created, err)
	}

	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := svc.Complete(context.Background(), op.OperationID, operation.StatusSucceeded, 200, []byte(`{"success":true,"code":"0"}`)); err != nil {
			t.Errorf("Complete 失败: %v", err)
		}
	}()

	final, err := svc.WaitTerminal(ctx, op.OperationID, 5*time.Second)
	if err != nil {
		t.Fatalf("WaitTerminal 失败: %v", err)
	}
	if final.Status != operation.StatusSucceeded {
		t.Fatalf("终态 = %s, want succeeded", final.Status)
	}
	if final.ResponseCode != 200 {
		t.Fatalf("重放响应码 = %d, want 200", final.ResponseCode)
	}
	// jsonb 存储会重排键顺序，按语义比较
	var stored map[string]any
	if err := json.Unmarshal(final.ResponsePayload, &stored); err != nil {
		t.Fatalf("重放响应不是合法 JSON: %v", err)
	}
	if stored["success"] != true || stored["code"] != "0" {
		t.Fatalf("重放响应内容不符: %s", string(final.ResponsePayload))
	}
}
