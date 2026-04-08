package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/trace"
	"workflow_qoder/internal/models"
)

// AZNSPClient AZ NSP HTTP客户端
type AZNSPClient struct {
	httpClient   *http.Client
	tracedClient *trace.TracedClient
	signer       *auth.Signer
}

// NewAZNSPClient 创建AZ NSP客户端
// signer: 当mTLS未启用时用于AK/SK签名，mTLS启用时传nil
func NewAZNSPClient(httpClient *http.Client, signer *auth.Signer) *AZNSPClient {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	return &AZNSPClient{
		httpClient: httpClient,
		signer:     signer,
	}
}

// NewAZNSPClientWithTrace 创建带链路追踪的AZ NSP客户端
// signer: 当mTLS未启用时用于AK/SK签名，mTLS启用时传nil
func NewAZNSPClientWithTrace(tracedClient *trace.TracedClient, httpClient *http.Client, signer *auth.Signer) *AZNSPClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &AZNSPClient{
		httpClient:   httpClient,
		tracedClient: tracedClient,
		signer:       signer,
	}
}

// CreateVPC 在指定AZ创建VPC
func (c *AZNSPClient) CreateVPC(ctx context.Context, azAddr string, req *models.VPCRequest) (*models.VPCResponse, error) {
	url := fmt.Sprintf("%s/api/v1/vpc", azAddr)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.do(httpReq)

	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var vpcResp models.VPCResponse
	err = json.Unmarshal(respBody, &vpcResp)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &vpcResp, nil
}

// CreateSubnet 在指定AZ创建子网
func (c *AZNSPClient) CreateSubnet(ctx context.Context, azAddr string, req *models.SubnetRequest) (*models.SubnetResponse, error) {
	url := fmt.Sprintf("%s/api/v1/subnet", azAddr)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.do(httpReq)

	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var subnetResp models.SubnetResponse
	err = json.Unmarshal(respBody, &subnetResp)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &subnetResp, nil
}

// HealthCheck 检查AZ NSP健康状态
func (c *AZNSPClient) HealthCheck(ctx context.Context, azAddr string) error {
	url := fmt.Sprintf("%s/api/v1/health", azAddr)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	resp, err := c.do(httpReq)

	if err != nil {
		return fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查失败，状态码: %d", resp.StatusCode)
	}

	return nil
}

// DeleteVPC 删除指定AZ的VPC（补偿操作）
func (c *AZNSPClient) DeleteVPC(ctx context.Context, azAddr string, vpcName string) error {
	url := fmt.Sprintf("%s/api/v1/vpc/%s", azAddr, vpcName)

	httpReq, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	resp, err := c.do(httpReq)

	if err != nil {
		return fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除VPC失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// GetVPCStatus 查询指定 AZ 的 VPC Worker 状态
func (c *AZNSPClient) GetVPCStatus(ctx context.Context, azAddr string, vpcName string) (*models.VPCStatusResponse, error) {
	url := fmt.Sprintf("%s/api/v1/vpc/%s/status", azAddr, vpcName)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	resp, err := c.do(httpReq)

	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("VPC %s not found in AZ", vpcName)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("查询失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var vpcStatus models.VPCStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&vpcStatus); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &vpcStatus, nil
}

// =====================================================
// PCCN Methods
// =====================================================

// CreatePCCN creates a PCCN connection in the specified AZ
func (c *AZNSPClient) CreatePCCN(ctx context.Context, azAddr string, req *models.PCCNRequest) (*models.PCCNResponse, error) {
	url := fmt.Sprintf("%s/api/v1/pccn", azAddr)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %v", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.do(httpReq)

	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var pccnResp models.PCCNResponse
	err = json.Unmarshal(respBody, &pccnResp)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &pccnResp, nil
}

// GetPCCNStatus queries the PCCN status in the specified AZ
func (c *AZNSPClient) GetPCCNStatus(ctx context.Context, azAddr string, pccnName string) (*models.PCCNStatusResponse, error) {
	url := fmt.Sprintf("%s/api/v1/pccn/%s/status", azAddr, pccnName)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	resp, err := c.do(httpReq)

	if err != nil {
		return nil, fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("PCCN %s not found in AZ", pccnName)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("查询失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	var pccnStatus models.PCCNStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&pccnStatus); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return &pccnStatus, nil
}

// DeletePCCN deletes a PCCN connection in the specified AZ
func (c *AZNSPClient) DeletePCCN(ctx context.Context, azAddr string, pccnName string) error {
	url := fmt.Sprintf("%s/api/v1/pccn/%s", azAddr, pccnName)

	httpReq, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	resp, err := c.do(httpReq)

	if err != nil {
		return fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除PCCN失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func (c *AZNSPClient) do(req *http.Request) (*http.Response, error) {
	// Only sign when AK/SK auth is needed (mTLS not active)
	if c.signer != nil {
		if err := c.signer.Sign(req); err != nil {
			return nil, fmt.Errorf("签名请求失败: %w", err)
		}
	}
	if c.tracedClient != nil {
		return c.tracedClient.Do(req)
	}
	return c.httpClient.Do(req)
}
