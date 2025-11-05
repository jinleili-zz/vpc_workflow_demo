package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"workflow_qoder/internal/models"
)

// AZNSPClient AZ NSP HTTP客户端
type AZNSPClient struct {
	httpClient *http.Client
}

// NewAZNSPClient 创建AZ NSP客户端
func NewAZNSPClient() *AZNSPClient {
	return &AZNSPClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
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

	resp, err := c.httpClient.Do(httpReq)
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

	resp, err := c.httpClient.Do(httpReq)
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

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查失败，状态码: %d", resp.StatusCode)
	}

	return nil
}
