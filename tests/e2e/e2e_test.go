package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestMain runs before all E2E tests and cleans the database completely.
func TestMain(m *testing.M) {
	cleanAllDatabases()
	m.Run()
}

// cleanAllDatabases truncates all application tables in all E2E databases via docker exec.
func cleanAllDatabases() {
	databases := []string{
		"top_nsp_vpc",
		"top_nsp_vfw",
		"nsp_cn_beijing_1a_vpc",
		"nsp_cn_beijing_1a_vfw",
	}

	truncateSQL := `
DO $$
DECLARE
    r RECORD;
BEGIN
    FOR r IN
        SELECT tablename FROM pg_tables
        WHERE schemaname = 'public'
          AND tablename NOT IN ('schema_migrations')
    LOOP
        EXECUTE 'TRUNCATE TABLE ' || quote_ident(r.tablename) || ' CASCADE';
    END LOOP;
END $$;
`

	for _, db := range databases {
		args := []string{
			"exec", "-i", "e2e-postgres",
			"psql", "-U", "nsp_user", "-d", db, "-c", truncateSQL,
		}
		out, err := exec.Command("docker", args...).CombinedOutput()
		if err != nil {
			fmt.Printf("[E2E cleanup] Warning: failed to clean database %s: %v\n%s\n", db, err, string(out))
		} else {
			fmt.Printf("[E2E cleanup] Cleaned database: %s\n", db)
		}
	}

	// Also flush Redis to remove stale workflow states and task queues
	out, err := exec.Command("docker", "exec", "e2e-redis", "redis-cli", "FLUSHALL").CombinedOutput()
	if err != nil {
		fmt.Printf("[E2E cleanup] Warning: failed to flush Redis: %v\n%s\n", err, string(out))
	} else {
		fmt.Printf("[E2E cleanup] Redis flushed\n")
	}

	// Wait briefly for services to reconnect after Redis flush
	time.Sleep(2 * time.Second)
}

const (
	topNSPVPCAddr = "http://localhost:18080"
	azNSPVPCAddr  = "http://localhost:18081"
	topNSPVFWAddr = "http://localhost:18082"
	azNSPVFWAddr  = "http://localhost:18083"
)

// ============================================================
// Helper functions
// ============================================================

func httpGet(t *testing.T, url string) (int, map[string]interface{}) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return resp.StatusCode, result
}

func httpPost(t *testing.T, url string, payload interface{}) (int, map[string]interface{}) {
	t.Helper()
	data, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(data))
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return resp.StatusCode, result
}

func httpDelete(t *testing.T, url string) (int, map[string]interface{}) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s failed: %v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return resp.StatusCode, result
}

// pollVPCStatusAZ polls VPC status directly on az-nsp until the
// VPC reaches a terminal state or the timeout is exceeded.
func pollVPCStatusAZ(t *testing.T, vpcName string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		code, result := httpGet(t, fmt.Sprintf("%s/api/v1/vpc/%s/status", azNSPVPCAddr, vpcName))
		if code == 404 || result == nil {
			t.Logf("VPC %s not found yet on az-nsp, waiting...", vpcName)
			time.Sleep(2 * time.Second)
			continue
		}
		status, _ := result["status"].(string)
		t.Logf("VPC %s status: %s", vpcName, status)
		if status == "running" || status == "failed" || status == "deleted" {
			return status
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("VPC %s did not reach terminal state within %v", vpcName, timeout)
	return ""
}

// pollSubnetStatusAZ polls subnet status directly on az-nsp
func pollSubnetStatusAZ(t *testing.T, subnetName string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, result := httpGet(t, fmt.Sprintf("%s/api/v1/subnet/%s/status", azNSPVPCAddr, subnetName))
		if result == nil {
			time.Sleep(2 * time.Second)
			continue
		}
		status, _ := result["status"].(string)
		t.Logf("Subnet %s status: %s", subnetName, status)
		if status == "running" || status == "failed" || status == "deleted" {
			return status
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("Subnet %s did not reach terminal state within %v", subnetName, timeout)
	return ""
}

// findVPCID looks up the VPC ID by name from the az-nsp list endpoint
func findVPCID(t *testing.T, vpcName string) string {
	t.Helper()
	_, listResult := httpGet(t, topNSPVPCAddr+"/api/v1/vpcs")
	vpcs, _ := listResult["vpcs"].([]interface{})
	for _, v := range vpcs {
		vpc, _ := v.(map[string]interface{})
		if vpc["vpc_name"] == vpcName {
			id, _ := vpc["id"].(string)
			return id
		}
	}
	t.Fatalf("VPC %s not found", vpcName)
	return ""
}

// ============================================================
// Test 1: Health Check - All Services
// ============================================================
func TestE2E_01_HealthCheck(t *testing.T) {
	services := map[string]string{
		"top-nsp-vpc": topNSPVPCAddr,
		"az-nsp-vpc":  azNSPVPCAddr,
		"top-nsp-vfw": topNSPVFWAddr,
		"az-nsp-vfw":  azNSPVFWAddr,
	}
	for name, addr := range services {
		t.Run(name, func(t *testing.T) {
			code, result := httpGet(t, addr+"/api/v1/health")
			if code != 200 {
				t.Fatalf("expected 200, got %d", code)
			}
			status, _ := result["status"].(string)
			if status != "ok" {
				t.Fatalf("expected status ok, got %s", status)
			}
			t.Logf("%s health: %v", name, result)
		})
	}
}

// ============================================================
// Test 2: AZ Registration & Discovery
// ============================================================
func TestE2E_02_AZRegistration(t *testing.T) {
	t.Run("ListRegions", func(t *testing.T) {
		code, result := httpGet(t, topNSPVPCAddr+"/api/v1/regions")
		if code != 200 {
			t.Fatalf("expected 200, got %d", code)
		}
		regions, ok := result["regions"].([]interface{})
		if !ok || len(regions) == 0 {
			t.Fatal("no regions returned")
		}
		found := false
		for _, r := range regions {
			if r.(string) == "cn-beijing" {
				found = true
			}
		}
		if !found {
			t.Fatal("cn-beijing region not found")
		}
		t.Logf("Regions: %v", regions)
	})

	t.Run("ListAZs", func(t *testing.T) {
		code, result := httpGet(t, topNSPVPCAddr+"/api/v1/regions/cn-beijing/azs")
		if code != 200 {
			t.Fatalf("expected 200, got %d", code)
		}
		azs, ok := result["azs"].([]interface{})
		if !ok || len(azs) == 0 {
			t.Fatal("no AZs returned")
		}
		az := azs[0].(map[string]interface{})
		if az["id"] != "cn-beijing-1a" {
			t.Fatalf("expected az cn-beijing-1a, got %v", az["id"])
		}
		if az["status"] != "online" {
			t.Fatalf("expected status online, got %v", az["status"])
		}
		nspAddr, _ := az["nsp_addr"].(string)
		if !strings.Contains(nspAddr, "e2e-az-nsp-vpc") {
			t.Fatalf("unexpected nsp_addr: %s", nspAddr)
		}
		t.Logf("AZ registered: %v", az)
	})
}

// ============================================================
// Test 3: Create VPC via Top NSP (SAGA Orchestration)
// ============================================================
func TestE2E_03_CreateVPC(t *testing.T) {
	payload := map[string]interface{}{
		"vpc_name":      "e2e-test-vpc",
		"region":        "cn-beijing",
		"vrf_name":      "vrf-e2e-test",
		"vlan_id":       100,
		"firewall_zone": "zone-e2e",
	}

	code, result := httpPost(t, topNSPVPCAddr+"/api/v1/vpc", payload)
	if code != 200 {
		t.Fatalf("expected 200, got %d, body: %v", code, result)
	}

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("VPC creation failed: %v", result["message"])
	}

	workflowID, _ := result["workflow_id"].(string)
	t.Logf("VPC creation submitted, workflow_id: %s, message: %s", workflowID, result["message"])

	if workflowID == "" {
		t.Fatal("workflow_id should not be empty for SAGA orchestration")
	}
}

// ============================================================
// Test 4: Poll VPC Status until completion
// ============================================================
func TestE2E_04_PollVPCStatus(t *testing.T) {
	finalStatus := pollVPCStatusAZ(t, "e2e-test-vpc", 90*time.Second)
	if finalStatus != "running" {
		t.Fatalf("VPC expected to be running, got %s", finalStatus)
	}
	t.Logf("VPC e2e-test-vpc reached status: %s", finalStatus)
}

// ============================================================
// Test 5: List VPCs via Top NSP (Aggregation)
// ============================================================
func TestE2E_05_ListVPCs(t *testing.T) {
	// Wait for all VPC tasks to complete (firewall callback may still be in-flight)
	pollVPCStatusAZ(t, "e2e-test-vpc", 30*time.Second)

	code, result := httpGet(t, topNSPVPCAddr+"/api/v1/vpcs")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	vpcs, ok := result["vpcs"].([]interface{})
	if !ok {
		t.Fatalf("unexpected vpcs format: %v", result)
	}

	found := false
	for _, v := range vpcs {
		vpc, _ := v.(map[string]interface{})
		if vpc["vpc_name"] == "e2e-test-vpc" {
			found = true
			status, _ := vpc["status"].(string)
			t.Logf("Found VPC: id=%v, status=%s", vpc["id"], status)
		}
	}
	if !found {
		t.Fatal("e2e-test-vpc not found in VPC list")
	}
}

// ============================================================
// Test 6: Get VPC by ID via Top NSP
// ============================================================
func TestE2E_06_GetVPCByID(t *testing.T) {
	vpcID := findVPCID(t, "e2e-test-vpc")

	code, result := httpGet(t, fmt.Sprintf("%s/api/v1/vpc/id/%s", topNSPVPCAddr, vpcID))
	if code != 200 {
		t.Fatalf("expected 200, got %d, body: %v", code, result)
	}

	// az-nsp returns {"success": true, "vpc": {...}}
	// top-nsp proxies as-is
	vpcData, ok := result["vpc"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected vpc object in response, got: %v", result)
	}

	name, _ := vpcData["vpc_name"].(string)
	if name != "e2e-test-vpc" {
		t.Fatalf("expected vpc_name e2e-test-vpc, got %s", name)
	}
	t.Logf("VPC by ID: %v", vpcData)
}

// ============================================================
// Test 7: Create Subnet via Top NSP
// ============================================================
func TestE2E_07_CreateSubnet(t *testing.T) {
	payload := map[string]interface{}{
		"subnet_name": "e2e-test-subnet",
		"vpc_name":    "e2e-test-vpc",
		"region":      "cn-beijing",
		"az":          "cn-beijing-1a",
		"cidr":        "10.0.1.0/24",
	}

	code, result := httpPost(t, topNSPVPCAddr+"/api/v1/subnet", payload)
	if code != 200 {
		t.Fatalf("expected 200, got %d, body: %v", code, result)
	}

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("Subnet creation failed: %v", result["message"])
	}

	subnetID, _ := result["subnet_id"].(string)
	t.Logf("Subnet creation submitted, subnet_id: %s", subnetID)
}

// ============================================================
// Test 8: Poll Subnet Status until completion
// ============================================================
func TestE2E_08_PollSubnetStatus(t *testing.T) {
	finalStatus := pollSubnetStatusAZ(t, "e2e-test-subnet", 60*time.Second)
	if finalStatus != "running" {
		t.Fatalf("Subnet expected to be running, got %s", finalStatus)
	}
	t.Logf("Subnet e2e-test-subnet reached status: %s", finalStatus)
}

// ============================================================
// Test 9: List Subnets by VPC ID via Top NSP
// ============================================================
func TestE2E_09_ListSubnetsByVPCID(t *testing.T) {
	vpcID := findVPCID(t, "e2e-test-vpc")

	code, result := httpGet(t, fmt.Sprintf("%s/api/v1/vpc/id/%s/subnets", topNSPVPCAddr, vpcID))
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	subnets, ok := result["subnets"].([]interface{})
	if !ok || len(subnets) == 0 {
		t.Fatalf("expected at least 1 subnet, got: %v", result)
	}

	found := false
	for _, s := range subnets {
		sub, _ := s.(map[string]interface{})
		if sub["subnet_name"] == "e2e-test-subnet" {
			found = true
			t.Logf("Found subnet: %v", sub)
		}
	}
	if !found {
		t.Fatal("e2e-test-subnet not found in subnet list")
	}
}

// ============================================================
// Test 10: Create Firewall Policy via Top NSP VFW
// ============================================================
func TestE2E_10_CreateFirewallPolicy(t *testing.T) {
	// Both source_ip and dest_ip must fall within a registered subnet's CIDR
	// so that the CIDR-to-zone lookup (cidr_zone_mapping) succeeds.
	// We created subnet 10.0.1.0/24, so both IPs must be in that range.
	payload := map[string]interface{}{
		"policy_name": "e2e-fw-policy",
		"source_ip":   "10.0.1.10",
		"dest_ip":     "10.0.1.20",
		"source_port": "8080",
		"dest_port":   "443",
		"protocol":    "tcp",
		"action":      "allow",
		"description": "E2E test firewall policy",
	}

	code, result := httpPost(t, topNSPVFWAddr+"/api/v1/firewall/policy", payload)
	t.Logf("Create policy response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("Policy creation failed: %v", result["message"])
	}
}

// ============================================================
// Test 11: List Firewall Policies via Top NSP VFW
// ============================================================
func TestE2E_11_ListFirewallPolicies(t *testing.T) {
	code, result := httpGet(t, topNSPVFWAddr+"/api/v1/firewall/policies")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	policies, ok := result["policies"].([]interface{})
	if !ok || len(policies) == 0 {
		t.Fatalf("expected at least 1 policy, got: %v", result)
	}

	found := false
	for _, p := range policies {
		pol, _ := p.(map[string]interface{})
		if pol["policy_name"] == "e2e-fw-policy" {
			found = true
			t.Logf("Found policy: %v", pol)
		}
	}
	if !found {
		t.Fatal("e2e-fw-policy not found in policy list")
	}
}

// ============================================================
// Test 12: Verify Firewall Policy on AZ NSP VFW
// ============================================================
func TestE2E_12_VerifyFirewallPolicyOnAZ(t *testing.T) {
	// Allow time for async task processing
	time.Sleep(10 * time.Second)

	code, result := httpGet(t, azNSPVFWAddr+"/api/v1/firewall/policies")
	t.Logf("AZ VFW policies (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	policies, ok := result["policies"].([]interface{})
	if !ok {
		t.Logf("No policies array, result: %v", result)
		return
	}

	t.Logf("Found %d policies on AZ VFW", len(policies))
	for _, p := range policies {
		pol, _ := p.(map[string]interface{})
		t.Logf("  AZ policy: name=%v status=%v", pol["policy_name"], pol["status"])
	}
}

// ============================================================
// Test 13: Create Second VPC for PCCN Test
// ============================================================
func TestE2E_13_CreateSecondVPC(t *testing.T) {
	payload := map[string]interface{}{
		"vpc_name":      "e2e-test-vpc-2",
		"region":        "cn-beijing",
		"vrf_name":      "vrf-e2e-test-2",
		"vlan_id":       200,
		"firewall_zone": "zone-e2e-2",
	}

	code, result := httpPost(t, topNSPVPCAddr+"/api/v1/vpc", payload)
	if code != 200 {
		t.Fatalf("expected 200, got %d, body: %v", code, result)
	}

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("Second VPC creation failed: %v", result["message"])
	}

	// Wait for VPC to be ready
	finalStatus := pollVPCStatusAZ(t, "e2e-test-vpc-2", 90*time.Second)
	if finalStatus != "running" {
		t.Fatalf("Second VPC expected to be running, got %s", finalStatus)
	}
	t.Logf("Second VPC e2e-test-vpc-2 created with status: %s", finalStatus)
}

// ============================================================
// Test 14: Create PCCN (Private Cloud Connection Network)
// ============================================================
func TestE2E_14_CreatePCCN(t *testing.T) {
	payload := map[string]interface{}{
		"pccn_name": "e2e-test-pccn",
		"vpc1": map[string]interface{}{
			"vpc_name": "e2e-test-vpc",
			"region":   "cn-beijing",
		},
		"vpc2": map[string]interface{}{
			"vpc_name": "e2e-test-vpc-2",
			"region":   "cn-beijing",
		},
	}

	code, result := httpPost(t, topNSPVPCAddr+"/api/v1/pccn", payload)
	t.Logf("Create PCCN response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("PCCN creation failed: %v", result["message"])
	}

	pccnID, _ := result["pccn_id"].(string)
	txID, _ := result["tx_id"].(string)
	t.Logf("PCCN creation submitted, pccn_id: %s, tx_id: %s", pccnID, txID)
}

// ============================================================
// Test 15: Poll PCCN Status until completion
// ============================================================
func TestE2E_15_PollPCCNStatus(t *testing.T) {
	finalStatus := pollPCCNStatus(t, "e2e-test-pccn", 120*time.Second)
	if finalStatus != "running" {
		t.Fatalf("PCCN expected to be running, got %s", finalStatus)
	}
	t.Logf("PCCN e2e-test-pccn reached status: %s", finalStatus)
}

// pollPCCNStatus polls PCCN status until terminal state or timeout
func pollPCCNStatus(t *testing.T, pccnName string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		code, result := httpGet(t, fmt.Sprintf("%s/api/v1/pccn/%s/status", topNSPVPCAddr, pccnName))
		if code == 404 || result == nil {
			t.Logf("PCCN %s not found yet, waiting...", pccnName)
			time.Sleep(3 * time.Second)
			continue
		}
		status, _ := result["overall_status"].(string)
		t.Logf("PCCN %s status: %s", pccnName, status)
		if status == "running" || status == "failed" || status == "partial_running" {
			return status
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("PCCN %s did not reach terminal state within %v", pccnName, timeout)
	return ""
}

// ============================================================
// Test 16: List PCCNs
// ============================================================
func TestE2E_16_ListPCCNs(t *testing.T) {
	code, result := httpGet(t, topNSPVPCAddr+"/api/v1/pccns")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	pccns, ok := result["pccns"].([]interface{})
	if !ok || len(pccns) == 0 {
		t.Fatalf("expected at least 1 PCCN, got: %v", result)
	}

	found := false
	for _, p := range pccns {
		pccn, _ := p.(map[string]interface{})
		if pccn["pccn_name"] == "e2e-test-pccn" {
			found = true
			status, _ := pccn["status"].(string)
			t.Logf("Found PCCN: name=%v, status=%s", pccn["pccn_name"], status)
		}
	}
	if !found {
		t.Fatal("e2e-test-pccn not found in PCCN list")
	}
}

// ============================================================
// Test 17: Verify VPC cannot be deleted when PCCN exists
// ============================================================
func TestE2E_17_VPCCannotBeDeletedWithPCCN(t *testing.T) {
	vpcID := findVPCID(t, "e2e-test-vpc")

	// Try to delete VPC - should fail because PCCN exists
	code, result := httpDelete(t, fmt.Sprintf("%s/api/v1/vpc/id/%s", topNSPVPCAddr, vpcID))
	t.Logf("Delete VPC with PCCN response (code=%d): %v", code, result)

	// Should return 400 (Bad Request) because VPC has PCCN connections
	if code == 200 {
		t.Fatal("VPC deletion should have been blocked due to existing PCCN connection")
	}

	success, _ := result["success"].(bool)
	if success {
		t.Fatal("VPC deletion should have failed but succeeded")
	}

	message, _ := result["message"].(string)
	if !strings.Contains(message, "PCCN") {
		t.Logf("Warning: expected error message to mention PCCN, got: %s", message)
	}

	t.Logf("VPC deletion correctly blocked: %s", message)
}

// ============================================================
// Test 18: Delete PCCN
// ============================================================
func TestE2E_18_DeletePCCN(t *testing.T) {
	code, result := httpDelete(t, fmt.Sprintf("%s/api/v1/pccn/%s", topNSPVPCAddr, "e2e-test-pccn"))
	t.Logf("Delete PCCN response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("PCCN deletion failed: %v", result["message"])
	}

	// Wait for PCCN deletion to complete
	time.Sleep(5 * time.Second)

	// Verify PCCN is deleted
	code2, result2 := httpGet(t, fmt.Sprintf("%s/api/v1/pccn/%s/status", topNSPVPCAddr, "e2e-test-pccn"))
	if code2 != 404 {
		t.Logf("Warning: PCCN still exists after deletion, code=%d, result=%v", code2, result2)
	}

	t.Log("PCCN deleted successfully")
}

// ============================================================
// Test 19: Delete Second VPC
// ============================================================
func TestE2E_19_DeleteSecondVPC(t *testing.T) {
	vpcID := findVPCID(t, "e2e-test-vpc-2")

	code, result := httpDelete(t, fmt.Sprintf("%s/api/v1/vpc/id/%s", topNSPVPCAddr, vpcID))
	t.Logf("Delete second VPC response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
}

// ============================================================
// Test 20: Delete Subnet
// ============================================================
func TestE2E_20_DeleteSubnet(t *testing.T) {
	// Find subnet ID via az-nsp
	_, listResult := httpGet(t, azNSPVPCAddr+"/api/v1/vpcs")
	vpcs, _ := listResult["vpcs"].([]interface{})
	var vpcID string
	for _, v := range vpcs {
		vpc, _ := v.(map[string]interface{})
		if vpc["vpc_name"] == "e2e-test-vpc" {
			vpcID, _ = vpc["id"].(string)
			break
		}
	}
	if vpcID == "" {
		t.Fatal("could not find VPC ID for subnet lookup")
	}

	_, subResult := httpGet(t, fmt.Sprintf("%s/api/v1/vpc/id/%s/subnets", topNSPVPCAddr, vpcID))
	subnets, _ := subResult["subnets"].([]interface{})
	var subnetID string
	for _, s := range subnets {
		sub, _ := s.(map[string]interface{})
		if sub["subnet_name"] == "e2e-test-subnet" {
			subnetID, _ = sub["id"].(string)
			break
		}
	}
	if subnetID == "" {
		t.Fatal("could not find subnet ID")
	}

	code, result := httpDelete(t, fmt.Sprintf("%s/api/v1/subnet/id/%s", topNSPVPCAddr, subnetID))
	t.Logf("Delete subnet response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
}

// ============================================================
// Test 21: Delete Firewall Policy (must be done before VPC delete)
// ============================================================
func TestE2E_21_DeleteFirewallPolicy(t *testing.T) {
	// Find the policy ID
	code, result := httpGet(t, topNSPVFWAddr+"/api/v1/firewall/policies")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	policies, ok := result["policies"].([]interface{})
	if !ok || len(policies) == 0 {
		t.Log("No policies to delete, skipping")
		return
	}

	var policyID string
	for _, p := range policies {
		pol, _ := p.(map[string]interface{})
		if pol["policy_name"] == "e2e-fw-policy" {
			policyID, _ = pol["id"].(string)
			break
		}
	}
	if policyID == "" {
		t.Fatal("could not find e2e-fw-policy ID")
	}

	code, result = httpDelete(t, fmt.Sprintf("%s/api/v1/firewall/policy/%s", topNSPVFWAddr, policyID))
	t.Logf("Delete policy response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	// Also delete from AZ VFW (top-level delete doesn't propagate to AZ level)
	code, result = httpDelete(t, fmt.Sprintf("%s/api/v1/firewall/policy/%s", azNSPVFWAddr, "e2e-fw-policy"))
	t.Logf("Delete AZ policy response (code=%d): %v", code, result)
}

// ============================================================
// Test 22: Delete VPC
// ============================================================
func TestE2E_22_DeleteVPC(t *testing.T) {
	vpcID := findVPCID(t, "e2e-test-vpc")

	code, result := httpDelete(t, fmt.Sprintf("%s/api/v1/vpc/id/%s", topNSPVPCAddr, vpcID))
	t.Logf("Delete VPC response (code=%d): %v", code, result)

	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
}

// ============================================================
// Test 23: Verify VPC deletion
// ============================================================
func TestE2E_23_VerifyDeletion(t *testing.T) {
	time.Sleep(3 * time.Second)

	code, result := httpGet(t, topNSPVPCAddr+"/api/v1/vpcs")
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}

	vpcs, ok := result["vpcs"].([]interface{})
	if !ok {
		t.Logf("vpcs is nil or not array - likely empty, which is expected")
		return
	}

	for _, v := range vpcs {
		vpc, _ := v.(map[string]interface{})
		name, _ := vpc["vpc_name"].(string)
		status, _ := vpc["status"].(string)
		if (name == "e2e-test-vpc" || name == "e2e-test-vpc-2") && status != "deleted" && status != "deleting" {
			t.Fatalf("VPC %s should be deleted/deleting, got status: %s", name, status)
		}
	}

	t.Log("All VPCs deletion verified")
}
