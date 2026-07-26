package orchestrator

import (
	"testing"

	"workflow_qoder/internal/models"
)

func TestTargetScopesMatchTopologyUniqueness(t *testing.T) {
	if first, second := vpcTargetScope(&models.VPCRequest{VPCName: "shared", Region: "region-a"}), vpcTargetScope(&models.VPCRequest{VPCName: "shared", Region: "region-b"}); first != second || first != "shared" {
		t.Fatalf("VPC target scopes = %q/%q, want shared global name", first, second)
	}
	if first, second := subnetTargetScope(&models.SubnetRequest{SubnetName: "subnet", AZ: "az-a", Region: "region-a"}), subnetTargetScope(&models.SubnetRequest{SubnetName: "subnet", AZ: "az-a", Region: "region-b"}); first != second || first != "az-a/subnet" {
		t.Fatalf("Subnet target scopes = %q/%q, want database (az,name) identity", first, second)
	}
	firstPCCN := &models.PCCNRequest{PCCNName: "shared", VPC1: models.VPCRef{VPCName: "a", Region: "r1"}, VPC2: models.VPCRef{VPCName: "b", Region: "r2"}}
	secondPCCN := &models.PCCNRequest{PCCNName: "shared", VPC1: models.VPCRef{VPCName: "b", Region: "r2"}, VPC2: models.VPCRef{VPCName: "a", Region: "r1"}}
	if first, second := pccnTargetScope(firstPCCN), pccnTargetScope(secondPCCN); first != second || first != "shared" {
		t.Fatalf("PCCN target scopes = %q/%q, want shared global name", first, second)
	}
}

func TestRunningPCCNDetailsPreservesAndBackfillsAZTargets(t *testing.T) {
	current := map[string]models.VPCDetail{
		"region-a/vpc-a": {Region: "region-a", AZs: []string{"az-a"}, Status: "creating"},
		"region-b/vpc-b": {Region: "region-b", Status: "creating"},
	}
	got := runningPCCNDetails(
		current,
		models.VPCRef{Region: "region-a", VPCName: "vpc-a"},
		models.VPCRef{Region: "region-b", VPCName: "vpc-b"},
		[]*models.AZ{
			{Region: "region-a", ID: "az-a"},
			{Region: "region-b", ID: "az-c"},
			{Region: "region-b", ID: "az-b"},
		},
	)
	first := got["region-a/vpc-a"]
	second := got["region-b/vpc-b"]
	if first.Status != "running" || len(first.AZs) != 1 || first.AZs[0] != "az-a" {
		t.Fatalf("preserved first target = %#v", first)
	}
	if second.Status != "running" || len(second.AZs) != 2 || second.AZs[0] != "az-b" || second.AZs[1] != "az-c" {
		t.Fatalf("backfilled second target = %#v", second)
	}
	if current["region-a/vpc-a"].Status != "creating" {
		t.Fatal("helper mutated persisted input snapshot")
	}
}
