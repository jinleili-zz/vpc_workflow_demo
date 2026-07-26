package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"workflow_qoder/internal/client"
	"workflow_qoder/internal/models"
	"workflow_qoder/internal/operation"
	topreconciler "workflow_qoder/internal/top/reconciler"
	vfwdao "workflow_qoder/internal/top/vfw/dao"
	topdao "workflow_qoder/internal/top/vpc/dao"

	"github.com/google/uuid"
	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/logger"
	"github.com/jinleili-zz/nsp-platform/trace"
)

type PolicyService struct {
	vpcDAO           *topdao.TopVPCDAO
	vfwDAO           *vfwdao.TopVFWDAO
	signer           *auth.Signer
	signedHTTP       *client.SignedTracedClient
	azRegistry       map[string]string
	operationService *operation.Service
	mu               sync.RWMutex
	reconcilerOnce   sync.Once
	executionRepo    *topreconciler.Repository
	executionRunner  *topreconciler.Reconciler
}

func NewPolicyService(vpcDB, vfwDB *sql.DB, signer *auth.Signer) *PolicyService {
	service := &PolicyService{
		vpcDAO:           topdao.NewTopVPCDAO(vpcDB),
		vfwDAO:           vfwdao.NewTopVFWDAO(vfwDB),
		signer:           signer,
		signedHTTP:       client.NewSignedTracedClient(trace.NewTracedClient(nil), signer),
		azRegistry:       make(map[string]string),
		operationService: operation.NewService(operation.NewRepository(vfwDB)),
	}
	service.executionRepo = topreconciler.NewRepository(vfwDB, "top-nsp-vfw")
	service.executionRunner = topreconciler.New(service.executionRepo, "top-nsp-vfw-"+uuid.NewString(), service.pollAZOperation, nil)
	return service
}

func (s *PolicyService) RegisterAZ(region, az, addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s:%s", region, az)
	s.azRegistry[key] = addr
	logger.Info("注册AZ", "key", key, "addr", addr)
}

func (s *PolicyService) GetAZAddr(region, az string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := fmt.Sprintf("%s:%s", region, az)
	addr, ok := s.azRegistry[key]
	return addr, ok
}

func (s *PolicyService) CreatePolicy(ctx context.Context, req *models.FirewallPolicyRequest) (*models.FirewallPolicyResponse, error) {
	logger.InfoContext(ctx, "开始创建防火墙策略", "policy_name", req.PolicyName)
	op, decision, err := s.beginOperation(ctx, req)
	if err != nil {
		return nil, err
	}
	if decision != operation.DecisionNew && len(op.ResponsePayload) > 0 {
		return replayPolicyResponse(op), nil
	}
	lease, claimed, err := s.operationService.ClaimDispatch(ctx, op.OperationID, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return replayPolicyResponse(op), nil
	}
	defer lease.Close()
	ctx = lease.Context(ctx)
	op.Status = operation.StatusDispatching
	op.Version++
	ctx = operation.ContextWithIdentity(ctx, operation.RequestIdentity{IdempotencyKey: op.IdempotencyKey, RootOperationID: op.RootOperationID, ParentOperationID: op.OperationID, ResourceGeneration: op.Generation})

	srcInfo, err := s.vpcDAO.FindZoneByIP(ctx, req.SourceIP)
	if err != nil {
		return s.failOperation(ctx, op, fmt.Sprintf("查询源IP信息失败: %v", err))
	}
	if srcInfo == nil {
		return s.failOperation(ctx, op, fmt.Sprintf("源IP %s 未找到对应的Zone信息，请确保该IP属于已创建的子网", req.SourceIP))
	}

	dstInfo, err := s.vpcDAO.FindZoneByIP(ctx, req.DestIP)
	if err != nil {
		return s.failOperation(ctx, op, fmt.Sprintf("查询目的IP信息失败: %v", err))
	}
	if dstInfo == nil {
		return s.failOperation(ctx, op, fmt.Sprintf("目的IP %s 未找到对应的Zone信息，请确保该IP属于已创建的子网", req.DestIP))
	}

	logger.InfoContext(ctx, "源IP解析", "source_ip", req.SourceIP, "zone", srcInfo.FirewallZone, "az", srcInfo.AZ)
	logger.InfoContext(ctx, "目的IP解析", "dest_ip", req.DestIP, "zone", dstInfo.FirewallZone, "az", dstInfo.AZ)

	policyID := op.ResourceID
	type policyTarget struct {
		region string
		az     string
		addr   string
	}
	targets := make(map[string]policyTarget)
	for _, target := range []struct{ region, az, label string }{
		{region: srcInfo.Region, az: srcInfo.AZ, label: "源"},
		{region: dstInfo.Region, az: dstInfo.AZ, label: "目的"},
	} {
		key := target.region + "/" + target.az
		if _, exists := targets[key]; exists {
			continue
		}
		addr, ok := s.GetAZAddr(target.region, target.az)
		if !ok {
			return s.failOperation(ctx, op, fmt.Sprintf("%sAZ %s/%s 未注册", target.label, target.region, target.az))
		}
		targets[key] = policyTarget{region: target.region, az: target.az, addr: addr}
	}

	policy := &models.PolicyRegistry{
		ID: policyID, PolicyName: req.PolicyName, SourceIP: req.SourceIP, DestIP: req.DestIP,
		SourcePort: req.SourcePort, DestPort: req.DestPort, Protocol: req.Protocol, Action: req.Action,
		Description: req.Description, SourceVPC: srcInfo.VPCName, DestVPC: dstInfo.VPCName,
		SourceZone: srcInfo.FirewallZone, DestZone: dstInfo.FirewallZone,
		SourceRegion: srcInfo.Region, DestRegion: dstInfo.Region, SourceAZ: srcInfo.AZ, DestAZ: dstInfo.AZ,
		Status: "creating",
	}
	if err := s.vfwDAO.CreatePolicy(ctx, policy); err != nil {
		// No child has been dispatched yet. Preserve dispatching so recovery can
		// idempotently verify/restore the ambiguous local commit.
		return nil, fmt.Errorf("persist recoverable policy topology: %w", err)
	}
	for key, target := range targets {
		recordID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(policyID+":"+key)).String()
		record := &models.PolicyAZRecord{ID: recordID, PolicyID: policyID, Region: target.region, AZ: target.az, Status: "creating"}
		if err := s.vfwDAO.CreateAZRecord(ctx, record); err != nil {
			// All AZ records are established before the first external call. A
			// retry can safely complete missing local facts without orphaning a child.
			return nil, fmt.Errorf("persist recoverable policy AZ record %s: %w", key, err)
		}
	}

	type azResult struct {
		region     string
		az         string
		policyID   string
		workflowID string
		err        error
		success    bool
		recorded   bool
	}

	var wg sync.WaitGroup
	resultChan := make(chan *azResult, len(targets))

	for _, target := range targets {
		wg.Add(1)
		go func(region, az, addr string) {
			defer wg.Done()

			azReq := &models.AZFirewallPolicyRequest{
				PolicyName:  req.PolicyName,
				SourceZone:  srcInfo.FirewallZone,
				DestZone:    dstInfo.FirewallZone,
				SourceIP:    req.SourceIP,
				DestIP:      req.DestIP,
				SourcePort:  req.SourcePort,
				DestPort:    req.DestPort,
				Protocol:    req.Protocol,
				Action:      req.Action,
				Description: req.Description,
				Region:      region,
				AZ:          az,
			}

			body, _ := json.Marshal(azReq)
			url := fmt.Sprintf("%s/api/v1/firewall/policy", addr)
			httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(body))
			if reqErr != nil {
				resultChan <- &azResult{region: region, az: az, err: reqErr, success: false}
				s.vfwDAO.UpdateAZRecord(ctx, policyID, region, az, "", "failed", reqErr.Error())
				return
			}
			httpReq.Header.Set("Content-Type", "application/json")
			childIdentity, identityErr := operation.DeriveChildIdentity(ctx, "POST /api/v1/firewall/policy", fmt.Sprintf("%s/%s/%s", region, az, req.PolicyName), azReq)
			if identityErr != nil {
				resultChan <- &azResult{region: region, az: az, err: identityErr, success: false}
				s.vfwDAO.UpdateAZRecord(ctx, policyID, region, az, "", "failed", identityErr.Error())
				return
			}
			operation.ApplyIdentityHeaders(httpReq.Header, childIdentity)
			resp, err := s.signedHTTP.Do(httpReq)

			result := &azResult{region: region, az: az}
			if err != nil {
				logger.WarnContext(ctx, "AZ创建失败", "az", az, "error", err)
				result.err = err
				result.success = false
				s.vfwDAO.UpdateAZRecord(ctx, policyID, region, az, "", "failed", err.Error())
			} else {
				defer resp.Body.Close()
				var azResp models.AZFirewallPolicyResponse
				json.NewDecoder(resp.Body).Decode(&azResp)

				if !azResp.Success {
					result.err = fmt.Errorf("%s", azResp.Message)
					result.success = false
					if azResp.OperationID != "" {
						if recordErr := s.executionRepo.RecordExecution(ctx, topreconciler.Execution{
							OperationID: op.OperationID, Region: region, AZ: az,
							ChildOperationID: azResp.OperationID,
						}); recordErr != nil {
							result.err = fmt.Errorf("record failed AZ child operation: %w", recordErr)
						} else {
							result.recorded = true
						}
					}
					s.vfwDAO.UpdateAZRecord(ctx, policyID, region, az, "", "failed", azResp.Message)
				} else {
					if azResp.OperationID == "" {
						result.err = fmt.Errorf("AZ %s response omitted child operation ID", az)
						result.success = false
						s.vfwDAO.UpdateAZRecord(ctx, policyID, region, az, azResp.PolicyID, "failed", result.err.Error())
						resultChan <- result
						return
					}
					if err := s.executionRepo.RecordExecution(ctx, topreconciler.Execution{
						OperationID: op.OperationID, Region: region, AZ: az,
						ChildOperationID: azResp.OperationID,
					}); err != nil {
						result.err = fmt.Errorf("record AZ child operation: %w", err)
						result.success = false
						resultChan <- result
						return
					}
					result.policyID = azResp.PolicyID
					result.workflowID = azResp.WorkflowID
					result.success = true
					result.recorded = true
					s.vfwDAO.UpdateAZRecord(ctx, policyID, region, az, azResp.PolicyID, "creating", "")
				}
			}
			resultChan <- result
		}(target.region, target.az, target.addr)
	}

	wg.Wait()
	close(resultChan)

	azResults := make(map[string]string)
	allSuccess := true
	allRecorded := true
	for result := range resultChan {
		key := result.region + "/" + result.az
		if result.success {
			azResults[key] = result.workflowID
		} else {
			azResults[key] = fmt.Sprintf("失败: %v", result.err)
			allSuccess = false
		}
		if !result.recorded {
			allRecorded = false
		}
	}

	if !allRecorded {
		_ = s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "creating", "部分AZ尚未形成可恢复的子操作")
		return nil, fmt.Errorf("recoverable partial AZ policy submission")
	}

	s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "creating", "")

	response := &models.FirewallPolicyResponse{
		Code:        "0",
		Success:     true,
		Message:     "防火墙策略工作流已提交",
		OperationID: op.OperationID,
		ResourceID:  policyID,
		Status:      string(operation.StatusRunning),
		PolicyID:    policyID,
		SourceZone:  srcInfo.FirewallZone,
		DestZone:    dstInfo.FirewallZone,
		AZResults:   azResults,
	}
	if !allSuccess {
		response.Message = "防火墙策略子操作已记录，等待终态聚合"
	}
	stored, err := s.operationService.StoreResponse(ctx, op.OperationID, operation.StatusRunning, "0", response)
	if err != nil {
		return nil, fmt.Errorf("store firewall operation response: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("firewall operation response was concurrently changed")
	}
	return response, nil
}

func (s *PolicyService) beginOperation(ctx context.Context, req *models.FirewallPolicyRequest) (*operation.Operation, operation.Decision, error) {
	identity, ok := operation.IdentityFromContext(ctx)
	if !ok || identity.IdempotencyKey == "" {
		return nil, "", fmt.Errorf("%w: Idempotency-Key is required", operation.ErrInvalidIdempotencyKey)
	}
	op, decision, err := s.operationService.BeginTarget(ctx, operation.BeginRequest{
		OwnerService: "top-nsp-vfw", CallerScope: "northbound:compat", RouteScope: "POST /api/v1/firewall/policy",
		OperationType: "apply_firewall_policy", TargetScope: req.PolicyName, IdempotencyKey: identity.IdempotencyKey,
		Payload: req, ResourceType: "firewall_policy", ResourceID: uuid.NewString(), Generation: 1,
	})
	if err != nil {
		return nil, "", err
	}
	if decision == operation.DecisionConflict {
		return op, decision, fmt.Errorf("%w: key is already associated with another request", operation.ErrIdempotencyKeyReused)
	}
	if decision == operation.DecisionResourceConflict {
		return op, decision, fmt.Errorf("%w: target resource already exists with another specification", operation.ErrResourceSpecConflict)
	}
	if decision == operation.DecisionResourceBusy {
		return op, decision, fmt.Errorf("%w: target resource deletion is in progress", operation.ErrResourceOperationInProgress)
	}
	return op, decision, nil
}

func replayPolicyResponse(op *operation.Operation) *models.FirewallPolicyResponse {
	if len(op.ResponsePayload) > 0 {
		var response models.FirewallPolicyResponse
		if json.Unmarshal(op.ResponsePayload, &response) == nil {
			return &response
		}
	}
	return &models.FirewallPolicyResponse{Code: "0", Success: true, Message: "Firewall policy operation already accepted", OperationID: op.OperationID, ResourceID: op.ResourceID, PolicyID: op.ResourceID, Status: string(op.Status)}
}

func (s *PolicyService) failOperation(ctx context.Context, op *operation.Operation, message string) (*models.FirewallPolicyResponse, error) {
	response := &models.FirewallPolicyResponse{Code: "OPERATION_FAILED", Success: false, Message: message, OperationID: op.OperationID, ResourceID: op.ResourceID, PolicyID: op.ResourceID, Status: string(operation.StatusFailed)}
	stored, err := s.operationService.StoreResponseAndReleaseTarget(ctx, op.OperationID, operation.StatusFailed, response.Code, response)
	if err != nil {
		return nil, fmt.Errorf("store failed firewall operation: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("failed firewall operation was concurrently changed")
	}
	return response, nil
}

func (s *PolicyService) failOperationWithResult(ctx context.Context, op *operation.Operation, message, sourceZone, destZone string, azResults map[string]string) (*models.FirewallPolicyResponse, error) {
	response := &models.FirewallPolicyResponse{Code: "OPERATION_FAILED", Success: false, Message: message, OperationID: op.OperationID, ResourceID: op.ResourceID, PolicyID: op.ResourceID, Status: string(operation.StatusFailed), SourceZone: sourceZone, DestZone: destZone, AZResults: azResults}
	stored, err := s.operationService.StoreResponse(ctx, op.OperationID, operation.StatusFailed, response.Code, response)
	if err != nil {
		return nil, fmt.Errorf("store failed firewall operation: %w", err)
	}
	if !stored {
		return nil, fmt.Errorf("failed firewall operation was concurrently changed")
	}
	return response, nil
}

func (s *PolicyService) GetOperation(ctx context.Context, operationID string) (*operation.Operation, error) {
	return s.operationService.Get(ctx, operationID)
}

func (s *PolicyService) StartReconciler(ctx context.Context, interval time.Duration) {
	if interval <= 0 || s.operationService == nil {
		return
	}
	s.reconcilerOnce.Do(func() {
		go func() {
			s.reconcileDispatches()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.reconcileDispatches()
				}
			}
		}()
	})
}

func (s *PolicyService) reconcileDispatches() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operations, err := s.operationService.ListRecoverableDispatch(ctx, "top-nsp-vfw", 100)
	if err != nil {
		return
	}
	for _, op := range operations {
		var request models.FirewallPolicyRequest
		if json.Unmarshal(op.RequestPayload, &request) != nil {
			_ = s.operationService.DeferDispatch(ctx, op.OperationID)
			continue
		}
		source, sourceErr := s.vpcDAO.FindZoneByIP(ctx, request.SourceIP)
		destination, destinationErr := s.vpcDAO.FindZoneByIP(ctx, request.DestIP)
		if sourceErr != nil || destinationErr != nil || source == nil || destination == nil {
			_ = s.operationService.DeferDispatch(ctx, op.OperationID)
			continue
		}
		if _, ok := s.GetAZAddr(source.Region, source.AZ); !ok {
			_ = s.operationService.DeferDispatch(ctx, op.OperationID)
			continue
		}
		if _, ok := s.GetAZAddr(destination.Region, destination.AZ); !ok {
			_ = s.operationService.DeferDispatch(ctx, op.OperationID)
			continue
		}
		operationCtx := operation.ContextWithIdentity(ctx, operation.RequestIdentity{
			IdempotencyKey: op.IdempotencyKey, RootOperationID: op.RootOperationID,
			ParentOperationID: op.OperationID, ResourceGeneration: op.Generation,
		})
		_, _ = s.CreatePolicy(operationCtx, &request)
	}
	if s.executionRunner != nil {
		_, _ = s.executionRunner.RunOnce(ctx)
	}
}

func (s *PolicyService) pollAZOperation(ctx context.Context, execution topreconciler.Execution) (topreconciler.ChildResult, error) {
	addr, ok := s.GetAZAddr(execution.Region, execution.AZ)
	if !ok {
		return topreconciler.ChildResult{}, fmt.Errorf("AZ %s/%s is not registered", execution.Region, execution.AZ)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/api/v1/operations/%s", addr, execution.ChildOperationID), nil)
	if err != nil {
		return topreconciler.ChildResult{}, err
	}
	response, err := s.signedHTTP.Do(request)
	if err != nil {
		return topreconciler.ChildResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return topreconciler.ChildResult{}, fmt.Errorf("AZ operation query returned %d", response.StatusCode)
	}
	var child operation.Operation
	if err := json.NewDecoder(response.Body).Decode(&child); err != nil {
		return topreconciler.ChildResult{}, err
	}
	status := child.Status
	if status == operation.StatusAccepted || status == operation.StatusDispatching {
		status = operation.StatusRunning
	}
	return topreconciler.ChildResult{Status: status, ErrorCode: child.ErrorCode, ErrorMessage: child.ErrorMessage}, nil
}

func (s *PolicyService) GetPolicyStatus(ctx context.Context, policyID string) (*models.PolicyRegistry, []*models.PolicyAZRecord, error) {
	policy, err := s.vfwDAO.GetPolicyByID(ctx, policyID)
	if err != nil {
		return nil, nil, err
	}

	records, err := s.vfwDAO.GetAZRecords(ctx, policyID)
	if err != nil {
		return nil, nil, err
	}

	return policy, records, nil
}

func (s *PolicyService) DeletePolicy(ctx context.Context, policyID string) error {
	policy, err := s.vfwDAO.GetPolicyByID(ctx, policyID)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := s.operationService.MarkTargetRetiring(ctx, "top-nsp-vfw", "firewall_policy", policy.PolicyName); err != nil {
		return err
	}
	records, err := s.vfwDAO.GetAZRecords(ctx, policyID)
	if err != nil {
		return err
	}
	for _, record := range records {
		addr, ok := s.GetAZAddr(record.Region, record.AZ)
		if !ok {
			return fmt.Errorf("AZ %s/%s is not registered", record.Region, record.AZ)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/api/v1/firewall/policy/%s", addr, policy.PolicyName), nil)
		if err != nil {
			return err
		}
		response, err := s.signedHTTP.Do(request)
		if err != nil {
			return fmt.Errorf("delete policy in AZ %s/%s: %w", record.Region, record.AZ, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("delete policy in AZ %s/%s returned %d", record.Region, record.AZ, response.StatusCode)
		}
	}
	return s.vfwDAO.DeletePolicyAndReleaseTarget(ctx, policyID)
}

func (s *PolicyService) ListPolicies(ctx context.Context) ([]*models.PolicyRegistry, error) {
	return s.vfwDAO.ListPolicies(ctx)
}

func (s *PolicyService) CountPoliciesByZone(ctx context.Context, zone string) (int, error) {
	return s.vfwDAO.CountPoliciesByZone(ctx, zone)
}
