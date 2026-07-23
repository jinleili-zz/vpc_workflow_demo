// Package idempotency 是幂等改造的业务测试套件（设计文档第 15 节）。
// 覆盖场景：顺序/并发重复创建、同 Key 不同参数冲突、Saga Step 重试去重、
// 重复/并发 Reply 只推进一次、PCCN 同名并发创建无孤儿 Task、删除 ensure-absent。
//
// 依赖（由 scripts/test-idempotency.sh 拉起）：
//   - PostgreSQL: 127.0.0.1:15433（docker，库 top_nsp_vpc / nsp_test_az1_vpc）
//   - Redis:      127.0.0.1:16379（docker）
//   - top-nsp-vpc: http://127.0.0.1:19080（单实例）
//   - az-nsp-vpc:  http://127.0.0.1:19081（单实例，region-test/test-az1）
//   - worker:      switch/firewall 各 1 个实例
package idempotency

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

const (
	topDBName = "top_nsp_vpc"
	azDBName  = "nsp_test_az1_vpc"

	pgDSNFormat = "postgres://nsp_user:nsp_password@127.0.0.1:15433/%s?sslmode=disable"
	redisAddr   = "127.0.0.1:16379"

	topBaseURL = "http://127.0.0.1:19080"
	azBaseURL  = "http://127.0.0.1:19081"

	testRegion = "region-test"
	testAZ     = "test-az1"
)

func openDB(t *testing.T, dbName string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", fmt.Sprintf(pgDSNFormat, dbName))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func requirePostgres(t *testing.T) {
	t.Helper()
	db, err := sql.Open("postgres", fmt.Sprintf(pgDSNFormat, topDBName))
	if err != nil {
		t.Skipf("PostgreSQL 不可用: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Skipf("PostgreSQL 不可达（请先运行 scripts/test-idempotency.sh 或 docker compose）: %v", err)
	}
}

func requireServices(t *testing.T) {
	t.Helper()
	requirePostgres(t)
	for name, url := range map[string]string{"top-nsp": topBaseURL, "az-nsp": azBaseURL} {
		resp, err := http.Get(url + "/api/v1/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Skipf("%s 不可用（请先运行 scripts/test-idempotency.sh）: %v", name, err)
		}
		resp.Body.Close()
	}
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, uuid.NewString()[:8])
}

type httpResult struct {
	StatusCode int
	Body       []byte
}

func doJSON(t *testing.T, method, url string, payload any, headers map[string]string) httpResult {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求 %s %s 失败: %v", method, url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return httpResult{StatusCode: resp.StatusCode, Body: body}
}

func decodeBody(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, string(body))
	}
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("计数查询失败: %v, query=%s", err, query)
	}
	return n
}

// pollUntil 轮询直到条件满足或超时。
func pollUntil(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s (timeout=%s)", desc, timeout)
}

// waitAZVPCStatus 等待 AZ 侧 VPC 进入目标状态。
func waitAZVPCStatus(t *testing.T, vpcName, want string, timeout time.Duration) {
	t.Helper()
	pollUntil(t, timeout, fmt.Sprintf("AZ VPC %s 状态变为 %s", vpcName, want), func() bool {
		res := doJSON(t, http.MethodGet, fmt.Sprintf("%s/api/v1/vpc/%s/status", azBaseURL, vpcName), nil, nil)
		if res.StatusCode != http.StatusOK {
			return false
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(res.Body, &status); err != nil {
			return false
		}
		return status.Status == want
	})
}
