package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	topdao "workflow_qoder/internal/top/vpc/dao"
	vfwdao "workflow_qoder/internal/top/vfw/dao"
	"workflow_qoder/internal/models"

	"github.com/google/uuid"
	"github.com/paic/nsp-common/pkg/logger"
)

type PolicyService struct {
	vpcDAO *topdao.TopVPCDAO
	vfwDAO *vfwdao.TopVFWDAO
	azRegistry map[string]string
	mu         sync.RWMutex
}

func NewPolicyService(vpcDB, vfwDB *sql.DB) *PolicyService {
	return &PolicyService{
		vpcDAO:     topdao.NewTopVPCDAO(vpcDB),
		vfwDAO:     vfwdao.NewTopVFWDAO(vfwDB),
		azRegistry: make(map[string]string),
	}
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

	srcInfo, err := s.vpcDAO.FindZoneByIP(ctx, req.SourceIP)
	if err != nil {
		return &models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("查询源IP信息失败: %v", err),
		}, nil
	}
	if srcInfo == nil {
		return &models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("源IP %s 未找到对应的Zone信息，请确保该IP属于已创建的子网", req.SourceIP),
		}, nil
	}

	dstInfo, err := s.vpcDAO.FindZoneByIP(ctx, req.DestIP)
	if err != nil {
		return &models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("查询目的IP信息失败: %v", err),
		}, nil
	}
	if dstInfo == nil {
		return &models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("目的IP %s 未找到对应的Zone信息，请确保该IP属于已创建的子网", req.DestIP),
		}, nil
	}

	logger.InfoContext(ctx, "源IP解析", "source_ip", req.SourceIP, "zone", srcInfo.FirewallZone, "az", srcInfo.AZ)
	logger.InfoContext(ctx, "目的IP解析", "dest_ip", req.DestIP, "zone", dstInfo.FirewallZone, "az", dstInfo.AZ)

	policyID := uuid.New().String()
	policy := &models.PolicyRegistry{
		ID:           policyID,
		PolicyName:   req.PolicyName,
		SourceIP:     req.SourceIP,
		DestIP:       req.DestIP,
		SourcePort:   req.SourcePort,
		DestPort:     req.DestPort,
		Protocol:     req.Protocol,
		Action:       req.Action,
		Description:  req.Description,
		SourceVPC:    srcInfo.VPCName,
		DestVPC:      dstInfo.VPCName,
		SourceZone:   srcInfo.FirewallZone,
		DestZone:     dstInfo.FirewallZone,
		SourceRegion: srcInfo.Region,
		DestRegion:   dstInfo.Region,
		SourceAZ:     srcInfo.AZ,
		DestAZ:       dstInfo.AZ,
		Status:       "creating",
	}

	if err := s.vfwDAO.CreatePolicy(ctx, policy); err != nil {
		return &models.FirewallPolicyResponse{
			Success: false,
			Message: fmt.Sprintf("创建策略记录失败: %v", err),
		}, nil
	}

	targetAZs := make(map[string]string)
	if srcInfo.AZ == dstInfo.AZ {
		addr, ok := s.GetAZAddr(srcInfo.Region, srcInfo.AZ)
		if !ok {
			s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "failed", fmt.Sprintf("AZ %s 未注册", srcInfo.AZ))
			return &models.FirewallPolicyResponse{
				Success: false,
				Message: fmt.Sprintf("AZ %s 未注册", srcInfo.AZ),
			}, nil
		}
		targetAZs[srcInfo.AZ] = addr
	} else {
		srcAddr, ok := s.GetAZAddr(srcInfo.Region, srcInfo.AZ)
		if !ok {
			s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "failed", fmt.Sprintf("源AZ %s 未注册", srcInfo.AZ))
			return &models.FirewallPolicyResponse{
				Success: false,
				Message: fmt.Sprintf("源AZ %s 未注册", srcInfo.AZ),
			}, nil
		}
		targetAZs[srcInfo.AZ] = srcAddr

		dstAddr, ok := s.GetAZAddr(dstInfo.Region, dstInfo.AZ)
		if !ok {
			s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "failed", fmt.Sprintf("目的AZ %s 未注册", dstInfo.AZ))
			return &models.FirewallPolicyResponse{
				Success: false,
				Message: fmt.Sprintf("目的AZ %s 未注册", dstInfo.AZ),
			}, nil
		}
		targetAZs[dstInfo.AZ] = dstAddr
	}

	type azResult struct {
		az         string
		policyID   string
		workflowID string
		err        error
		success    bool
	}

	var wg sync.WaitGroup
	resultChan := make(chan *azResult, len(targetAZs))

	for az, addr := range targetAZs {
		wg.Add(1)
		go func(az, addr string) {
			defer wg.Done()

			recordID := uuid.New().String()
			record := &models.PolicyAZRecord{
				ID:       recordID,
				PolicyID: policyID,
				AZ:       az,
				Status:   "creating",
			}
			s.vfwDAO.CreateAZRecord(ctx, record)

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
				Region:      srcInfo.Region,
				AZ:          az,
			}

			body, _ := json.Marshal(azReq)
			url := fmt.Sprintf("%s/api/v1/firewall/policy", addr)
			resp, err := http.Post(url, "application/json", bytes.NewBuffer(body))

			result := &azResult{az: az}
			if err != nil {
				logger.WarnContext(ctx, "AZ创建失败", "az", az, "error", err)
				result.err = err
				result.success = false
				s.vfwDAO.UpdateAZRecord(ctx, policyID, az, "", "failed", err.Error())
			} else {
				defer resp.Body.Close()
				var azResp models.AZFirewallPolicyResponse
				json.NewDecoder(resp.Body).Decode(&azResp)

				if !azResp.Success {
					result.err = fmt.Errorf("%s", azResp.Message)
					result.success = false
					s.vfwDAO.UpdateAZRecord(ctx, policyID, az, "", "failed", azResp.Message)
				} else {
					result.policyID = azResp.PolicyID
					result.workflowID = azResp.WorkflowID
					result.success = true
					s.vfwDAO.UpdateAZRecord(ctx, policyID, az, azResp.PolicyID, "creating", "")
				}
			}
			resultChan <- result
		}(az, addr)
	}

	wg.Wait()
	close(resultChan)

	azResults := make(map[string]string)
	allSuccess := true
	for result := range resultChan {
		if result.success {
			azResults[result.az] = result.workflowID
		} else {
			azResults[result.az] = fmt.Sprintf("失败: %v", result.err)
			allSuccess = false
		}
	}

	if !allSuccess {
		s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "failed", "部分AZ创建失败")
		return &models.FirewallPolicyResponse{
			Success:    false,
			Message:    "部分AZ创建策略失败",
			PolicyID:   policyID,
			SourceZone: srcInfo.FirewallZone,
			DestZone:   dstInfo.FirewallZone,
			AZResults:  azResults,
		}, nil
	}

	s.vfwDAO.UpdatePolicyStatus(ctx, policyID, "running", "")

	return &models.FirewallPolicyResponse{
		Success:    true,
		Message:    "防火墙策略创建成功",
		PolicyID:   policyID,
		SourceZone: srcInfo.FirewallZone,
		DestZone:   dstInfo.FirewallZone,
		AZResults:  azResults,
	}, nil
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
	return s.vfwDAO.DeletePolicy(ctx, policyID)
}

func (s *PolicyService) ListPolicies(ctx context.Context) ([]*models.PolicyRegistry, error) {
	return s.vfwDAO.ListPolicies(ctx)
}

func (s *PolicyService) CountPoliciesByZone(ctx context.Context, zone string) (int, error) {
	return s.vfwDAO.CountPoliciesByZone(ctx, zone)
}
