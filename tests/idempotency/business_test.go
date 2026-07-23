package idempotency

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"
)

// =====================================================
// 北向（Top NSP）幂等场景
// =====================================================

// 顺序重复 POST：同 Key 同 Body 两次创建，返回同一 Operation/Resource/Saga，
// 且 AZ 侧只形成一套资源与任务。对应设计文档 15.2 节"顺序重复 POST 100 次"。
func TestBusinessTopSequentialDuplicateVPC(t *testing.T) {
	requireServices(t)
	topDB := openDB(t, topDBName)
	azDB := openDB(t, azDBName)

	key := uniqueName("nb-seq")
	req := models.VPCRequest{
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-seq",
		VLANId:       1001,
		FirewallZone: "zone-seq",
	}
	headers := map[string]string{"Idempotency-Key": key}

	res1 := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/vpc", req, headers)
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("首次创建 status=%d body=%s", res1.StatusCode, string(res1.Body))
	}
	var resp1 models.VPCResponse
	decodeBody(t, res1.Body, &resp1)
	if !resp1.Success || resp1.Code != operation.CodeSuccess || resp1.OperationID == "" {
		t.Fatalf("首次创建响应异常: %+v", resp1)
	}

	res2 := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/vpc", req, headers)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("重复创建 status=%d body=%s", res2.StatusCode, string(res2.Body))
	}
	var resp2 models.VPCResponse
	decodeBody(t, res2.Body, &resp2)

	if resp2.OperationID != resp1.OperationID {
		t.Fatalf("重复请求返回不同 operation_id: %s != %s", resp2.OperationID, resp1.OperationID)
	}
	if resp2.VPCID != resp1.VPCID {
		t.Fatalf("重复请求返回不同 vpc_id: %s != %s", resp2.VPCID, resp1.VPCID)
	}
	if resp2.WorkflowID != resp1.WorkflowID {
		t.Fatalf("重复请求返回不同 saga_tx: %s != %s", resp2.WorkflowID, resp1.WorkflowID)
	}

	// Top 只创建一个 Operation
	if n := countRows(t, topDB,
		`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service='top-nsp-vpc' AND idempotency_key=$1`, key); n != 1 {
		t.Fatalf("Top Operation 数=%d, want 1", n)
	}

	// AZ 侧最终只有一套资源和 3 个任务（Saga 异步下发，轮询确认）
	pollUntil(t, 30*time.Second, "AZ 出现 VPC 资源", func() bool {
		return countRows(t, azDB, `SELECT COUNT(*) FROM vpc_resources WHERE vpc_name=$1 AND az=$2`, req.VPCName, testAZ) == 1
	})
	time.Sleep(2 * time.Second) // 等待可能的重复创建暴露
	if n := countRows(t, azDB, `SELECT COUNT(*) FROM vpc_resources WHERE vpc_name=$1 AND az=$2`, req.VPCName, testAZ); n != 1 {
		t.Fatalf("AZ VPC 资源数=%d, want 1", n)
	}
	if n := countRows(t, azDB,
		`SELECT COUNT(*) FROM tasks WHERE resource_id=$1`, resp1.VPCID); n != 3 {
		t.Fatalf("AZ 任务数=%d, want 3", n)
	}

	// 端到端：Saga 能识别 AZ 成功响应（code 协议修复），工作流真实执行到 running
	waitAZVPCStatus(t, req.VPCName, string(models.ResourceStatusRunning), 120*time.Second)

	// Operation 查询端点返回终态
	opRes := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/v1/operations/%s", topBaseURL, resp1.OperationID), nil, nil)
	if opRes.StatusCode != http.StatusOK {
		t.Fatalf("查询 Operation status=%d body=%s", opRes.StatusCode, string(opRes.Body))
	}
	var op operation.Operation
	decodeBody(t, opRes.Body, &op)
	if op.Status != operation.StatusSucceeded {
		t.Fatalf("Top Operation 状态=%s, want succeeded", op.Status)
	}

	// Top 注册表最终聚合为 running
	pollUntil(t, 120*time.Second, "Top vpc_registry 状态 running", func() bool {
		var status string
		err := topDB.QueryRow(`SELECT status FROM vpc_registry WHERE vpc_name=$1`, req.VPCName).Scan(&status)
		return err == nil && status == "running"
	})
}

// 并发重复 POST：20 个相同 Key 请求同时到达，只有一个 Operation 和一套资源。
// 对应设计文档 15.2 节"并发重复 POST 100 次"。
func TestBusinessTopConcurrentDuplicateVPC(t *testing.T) {
	requireServices(t)
	topDB := openDB(t, topDBName)
	azDB := openDB(t, azDBName)

	key := uniqueName("nb-conc")
	req := models.VPCRequest{
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-conc",
		VLANId:       1002,
		FirewallZone: "zone-conc",
	}

	const n = 20
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]httpResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = doJSON(t, http.MethodPost, topBaseURL+"/api/v1/vpc", req,
				map[string]string{"Idempotency-Key": key})
		}(i)
	}
	close(start)
	wg.Wait()

	opIDs := map[string]int{}
	vpcIDs := map[string]int{}
	for i, res := range results {
		if res.StatusCode != http.StatusOK {
			t.Fatalf("请求 %d status=%d body=%s", i, res.StatusCode, string(res.Body))
		}
		var resp models.VPCResponse
		decodeBody(t, res.Body, &resp)
		if !resp.Success {
			t.Fatalf("请求 %d success=false body=%s", i, string(res.Body))
		}
		opIDs[resp.OperationID]++
		vpcIDs[resp.VPCID]++
	}
	if len(opIDs) != 1 || len(vpcIDs) != 1 {
		t.Fatalf("并发请求返回多个标识: operation_ids=%v vpc_ids=%v", opIDs, vpcIDs)
	}

	if cnt := countRows(t, topDB,
		`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service='top-nsp-vpc' AND idempotency_key=$1`, key); cnt != 1 {
		t.Fatalf("Top Operation 数=%d, want 1", cnt)
	}

	pollUntil(t, 30*time.Second, "AZ 出现 VPC 资源", func() bool {
		return countRows(t, azDB, `SELECT COUNT(*) FROM vpc_resources WHERE vpc_name=$1 AND az=$2`, req.VPCName, testAZ) >= 1
	})
	time.Sleep(2 * time.Second)
	if cnt := countRows(t, azDB, `SELECT COUNT(*) FROM vpc_resources WHERE vpc_name=$1 AND az=$2`, req.VPCName, testAZ); cnt != 1 {
		t.Fatalf("AZ VPC 资源数=%d, want 1", cnt)
	}

	waitAZVPCStatus(t, req.VPCName, string(models.ResourceStatusRunning), 120*time.Second)
}

// 相同 Key 携带不同参数：返回 409 IDEMPOTENCY_KEY_REUSED，不覆盖原操作。
// 对应设计文档 15.2 节"相同 Key 不同 Body"。
func TestBusinessTopSameKeyDifferentBody(t *testing.T) {
	requireServices(t)

	key := uniqueName("nb-conflict")
	req := models.VPCRequest{
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-conflict",
		VLANId:       1003,
		FirewallZone: "zone-conflict",
	}
	headers := map[string]string{"Idempotency-Key": key}

	res1 := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/vpc", req, headers)
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("首次创建 status=%d body=%s", res1.StatusCode, string(res1.Body))
	}

	req2 := req
	req2.VLANId = 9999
	res2 := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/vpc", req2, headers)
	if res2.StatusCode != http.StatusConflict {
		t.Fatalf("同 Key 不同参数 status=%d, want 409; body=%s", res2.StatusCode, string(res2.Body))
	}
	var conflict struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
	}
	decodeBody(t, res2.Body, &conflict)
	if conflict.Code != operation.CodeIdempotencyKeyReused {
		t.Fatalf("冲突 code=%s, want %s", conflict.Code, operation.CodeIdempotencyKeyReused)
	}

	topDB := openDB(t, topDBName)
	if n := countRows(t, topDB,
		`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service='top-nsp-vpc' AND idempotency_key=$1`, key); n != 1 {
		t.Fatalf("冲突后 Operation 数=%d, want 1（未发生覆盖）", n)
	}
}

// =====================================================
// AZ 层幂等场景
// =====================================================

// Saga Step 重试：相同 X-Idempotency-Key（step.ID）重放，AZ 不创建第二套任务。
// 对应设计文档 15.2 节"Saga 请求成功但响应丢失"。
func TestBusinessAZSagaStepRetryDedup(t *testing.T) {
	requireServices(t)
	azDB := openDB(t, azDBName)

	stepKey := uuid.NewString() // 模拟 Saga step.ID
	req := models.VPCRequest{
		VPCID:        uuid.NewString(),
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-saga",
		VLANId:       1004,
		FirewallZone: "zone-saga",
	}
	headers := map[string]string{
		"X-Idempotency-Key":     stepKey,
		"X-Saga-Transaction-Id": uuid.NewString(),
	}

	res1 := doJSON(t, http.MethodPost, azBaseURL+"/api/v1/vpc", req, headers)
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("首次请求 status=%d body=%s", res1.StatusCode, string(res1.Body))
	}
	var resp1 models.VPCResponse
	decodeBody(t, res1.Body, &resp1)
	if !resp1.Success || resp1.Code != operation.CodeSuccess {
		t.Fatalf("首次请求失败: %+v", resp1)
	}

	// 模拟 Saga 在响应丢失后重试同一 Step
	res2 := doJSON(t, http.MethodPost, azBaseURL+"/api/v1/vpc", req, headers)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("Step 重试 status=%d body=%s", res2.StatusCode, string(res2.Body))
	}
	var resp2 models.VPCResponse
	decodeBody(t, res2.Body, &resp2)
	if resp2.OperationID != resp1.OperationID || resp2.VPCID != resp1.VPCID {
		t.Fatalf("Step 重试未重放首次结果: op=%s/%s vpc=%s/%s",
			resp1.OperationID, resp2.OperationID, resp1.VPCID, resp2.VPCID)
	}

	if n := countRows(t, azDB,
		`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service='az-nsp-vpc' AND idempotency_key=$1`, stepKey); n != 1 {
		t.Fatalf("AZ Operation 数=%d, want 1", n)
	}
	if n := countRows(t, azDB, `SELECT COUNT(*) FROM vpc_resources WHERE vpc_name=$1 AND az=$2`, req.VPCName, testAZ); n != 1 {
		t.Fatalf("AZ VPC 资源数=%d, want 1", n)
	}
	if n := countRows(t, azDB, `SELECT COUNT(*) FROM tasks WHERE resource_id=$1`, resp1.VPCID); n != 3 {
		t.Fatalf("AZ 任务数=%d, want 3（未创建第二套任务）", n)
	}

	waitAZVPCStatus(t, req.VPCName, string(models.ResourceStatusRunning), 120*time.Second)
}

// PCCN 同名并发创建：只有一个请求获胜，其余收到冲突，且无孤儿 Task。
// 对应设计文档 7.6 节与 15.2 节"PCCN 同名并发创建"。
func TestBusinessAZPCCNConcurrentSameName(t *testing.T) {
	requireServices(t)
	azDB := openDB(t, azDBName)

	// 前置：本端 VPC 必须存在（PCCN 编排依赖）
	vpcReq := models.VPCRequest{
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-pccn",
		VLANId:       1005,
		FirewallZone: "zone-pccn",
	}
	vpcRes := doJSON(t, http.MethodPost, azBaseURL+"/api/v1/vpc", vpcReq,
		map[string]string{"X-Idempotency-Key": uniqueName("az-vpc")})
	if vpcRes.StatusCode != http.StatusOK {
		t.Fatalf("创建前置 VPC 失败: %d %s", vpcRes.StatusCode, string(vpcRes.Body))
	}
	pollUntil(t, 30*time.Second, "前置 VPC 资源存在", func() bool {
		return countRows(t, azDB, `SELECT COUNT(*) FROM vpc_resources WHERE vpc_name=$1 AND az=$2`, vpcReq.VPCName, testAZ) == 1
	})

	pccnReq := models.PCCNRequest{
		PCCNName: uniqueName("idem-pccn"),
		VPC1:     models.VPCRef{VPCName: vpcReq.VPCName, Region: testRegion},
		VPC2:     models.VPCRef{VPCName: "peer-vpc", Region: "region-peer"},
	}

	const n = 10
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]httpResult, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			results[idx] = doJSON(t, http.MethodPost, azBaseURL+"/api/v1/pccn", pccnReq, nil)
		}(i)
	}
	close(start)
	wg.Wait()

	successes, conflicts := 0, 0
	for i, res := range results {
		var resp models.PCCNResponse
		decodeBody(t, res.Body, &resp)
		switch {
		case res.StatusCode == http.StatusOK && resp.Success:
			successes++
		case res.StatusCode == http.StatusBadRequest && resp.Code == operation.CodeResourceAlreadyExists:
			conflicts++
		default:
			t.Fatalf("请求 %d 出现意外响应: status=%d body=%s", i, res.StatusCode, string(res.Body))
		}
	}
	if successes != 1 || conflicts != n-1 {
		t.Fatalf("并发 PCCN 创建: 成功=%d 冲突=%d, want 1/%d", successes, conflicts, n-1)
	}

	// 只有一条资源、一套任务，且任务全部归属该资源（无孤儿 Task）
	pollUntil(t, 30*time.Second, "PCCN 任务创建", func() bool {
		return countRows(t, azDB, `
			SELECT COUNT(*) FROM tasks t
			JOIN pccn_resources p ON p.id = t.resource_id
			WHERE p.pccn_name=$1 AND p.az=$2`, pccnReq.PCCNName, testAZ) == 2
	})
	if cnt := countRows(t, azDB, `SELECT COUNT(*) FROM pccn_resources WHERE pccn_name=$1 AND az=$2`, pccnReq.PCCNName, testAZ); cnt != 1 {
		t.Fatalf("PCCN 资源数=%d, want 1", cnt)
	}
	if orphans := countRows(t, azDB, `
		SELECT COUNT(*) FROM tasks t
		WHERE t.resource_type='pccn'
		  AND t.created_at > NOW() - INTERVAL '10 minutes'
		  AND NOT EXISTS (SELECT 1 FROM pccn_resources p WHERE p.id = t.resource_id)`); orphans != 0 {
		t.Fatalf("存在 %d 个孤儿 PCCN 任务", orphans)
	}

	// 工作流可以正常执行完成
	pollUntil(t, 120*time.Second, "PCCN 状态 running", func() bool {
		res := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/v1/pccn/%s/status", azBaseURL, pccnReq.PCCNName), nil, nil)
		if res.StatusCode != http.StatusOK {
			return false
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := jsonUnmarshalQuiet(res.Body, &status); err != nil {
			return false
		}
		return status.Status == string(models.ResourceStatusRunning)
	})
}

// 删除 ensure-absent：重复删除、删除不存在资源都收敛到成功。
// 对应设计文档 15.2 节"相同 DELETE 重试"与 11.6 节删除语义。
func TestBusinessAZDeleteEnsureAbsent(t *testing.T) {
	requireServices(t)

	vpcReq := models.VPCRequest{
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-del",
		VLANId:       1006,
		FirewallZone: "zone-del",
	}
	createRes := doJSON(t, http.MethodPost, azBaseURL+"/api/v1/vpc", vpcReq,
		map[string]string{"X-Idempotency-Key": uniqueName("az-vpc-del")})
	if createRes.StatusCode != http.StatusOK {
		t.Fatalf("创建 VPC 失败: %d %s", createRes.StatusCode, string(createRes.Body))
	}
	waitAZVPCStatus(t, vpcReq.VPCName, string(models.ResourceStatusRunning), 120*time.Second)

	assertDeleteOK := func(name, desc string) {
		t.Helper()
		res := doJSON(t, http.MethodDelete, fmt.Sprintf("%s/api/v1/vpc/%s", azBaseURL, name), nil, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", desc, res.StatusCode, string(res.Body))
		}
		var resp struct {
			Success bool   `json:"success"`
			Code    string `json:"code"`
		}
		decodeBody(t, res.Body, &resp)
		if !resp.Success || resp.Code != operation.CodeSuccess {
			t.Fatalf("%s: resp=%+v, want success code=0", desc, resp)
		}
	}

	assertDeleteOK(vpcReq.VPCName, "首次删除")
	assertDeleteOK(vpcReq.VPCName, "重复删除（deleting/deleted 状态）")
	assertDeleteOK(uniqueName("nonexistent-vpc"), "删除不存在的 VPC")
}

// 北向 Subnet 幂等：Top 重试时 AZ 命中派生幂等键，不重复创建子网。
// 对应设计文档 7.2 节"AZ 已插入 Subnet 但 Top 未收到响应"。
func TestBusinessTopSubnetIdempotent(t *testing.T) {
	requireServices(t)
	azDB := openDB(t, azDBName)

	// 前置 VPC（Top 创建，等待 AZ 就绪）
	vpcKey := uniqueName("nb-vpc-for-subnet")
	vpcReq := models.VPCRequest{
		VPCName:      uniqueName("idem-vpc"),
		Region:       testRegion,
		VRFName:      "vrf-subnet",
		VLANId:       1007,
		FirewallZone: "zone-subnet",
	}
	vpcRes := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/vpc", vpcReq,
		map[string]string{"Idempotency-Key": vpcKey})
	if vpcRes.StatusCode != http.StatusOK {
		t.Fatalf("创建前置 VPC 失败: %d %s", vpcRes.StatusCode, string(vpcRes.Body))
	}
	waitAZVPCStatus(t, vpcReq.VPCName, string(models.ResourceStatusRunning), 120*time.Second)

	subnetKey := uniqueName("nb-subnet")
	subnetReq := models.SubnetRequest{
		SubnetName: uniqueName("idem-subnet"),
		VPCName:    vpcReq.VPCName,
		Region:     testRegion,
		AZ:         testAZ,
		CIDR:       "10.199.1.0/24",
	}
	headers := map[string]string{"Idempotency-Key": subnetKey}

	res1 := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/subnet", subnetReq, headers)
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("首次创建子网 status=%d body=%s", res1.StatusCode, string(res1.Body))
	}
	var resp1 models.SubnetResponse
	decodeBody(t, res1.Body, &resp1)
	if !resp1.Success || resp1.SubnetID == "" {
		t.Fatalf("首次创建子网失败: %+v", resp1)
	}

	res2 := doJSON(t, http.MethodPost, topBaseURL+"/api/v1/subnet", subnetReq, headers)
	var resp2 models.SubnetResponse
	decodeBody(t, res2.Body, &resp2)
	if res2.StatusCode != http.StatusOK || resp2.OperationID != resp1.OperationID || resp2.SubnetID != resp1.SubnetID {
		t.Fatalf("重复创建子网未重放: status=%d op=%s/%s subnet=%s/%s",
			res2.StatusCode, resp1.OperationID, resp2.OperationID, resp1.SubnetID, resp2.SubnetID)
	}

	if n := countRows(t, azDB, `SELECT COUNT(*) FROM subnet_resources WHERE subnet_name=$1 AND az=$2`, subnetReq.SubnetName, testAZ); n != 1 {
		t.Fatalf("AZ 子网资源数=%d, want 1", n)
	}
	// AZ 侧也只有一个派生 Operation
	if n := countRows(t, azDB,
		`SELECT COUNT(*) FROM orchestration_operations WHERE owner_service='az-nsp-vpc' AND route_scope='POST /api/v1/subnet' AND idempotency_key=$1`,
		"subnet:"+resp1.OperationID); n != 1 {
		t.Fatalf("AZ 派生 Operation 数=%d, want 1", n)
	}

	pollUntil(t, 120*time.Second, "子网状态 running", func() bool {
		res := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/v1/subnet/%s/status", azBaseURL, subnetReq.SubnetName), nil, nil)
		if res.StatusCode != http.StatusOK {
			return false
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := jsonUnmarshalQuiet(res.Body, &status); err != nil {
			return false
		}
		return status.Status == string(models.ResourceStatusRunning)
	})
}

func jsonUnmarshalQuiet(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
