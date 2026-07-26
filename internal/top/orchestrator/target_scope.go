package orchestrator

import (
	"fmt"

	"workflow_qoder/internal/models"
)

// Target scopes deliberately mirror the corresponding database uniqueness
// constraints. The request hash carries region/end-point specifications and
// turns a same-target/different-spec request into a conflict.
func vpcTargetScope(request *models.VPCRequest) string {
	return request.VPCName
}

func subnetTargetScope(request *models.SubnetRequest) string {
	return fmt.Sprintf("%s/%s", request.AZ, request.SubnetName)
}

func pccnTargetScope(request *models.PCCNRequest) string {
	return request.PCCNName
}
