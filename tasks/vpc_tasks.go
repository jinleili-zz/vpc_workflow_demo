package tasks

import (
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// VPCRequest VPC创建请求
type VPCRequest struct {
	VPCName     string `json:"vpc_name"`
	VPCID       string `json:"vpc_id"`
	VRFName     string `json:"vrf_name"`
	VLANId      int    `json:"vlan_id"`
	FirewallZone string `json:"firewall_zone"`
}

// CreateVRFOnSwitch 在交换机上创建VRF
func CreateVRFOnSwitch(requestJSON string) (string, error) {
	var req VPCRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[交换机任务] 开始创建VRF: %s (VPC: %s)", req.VRFName, req.VPCName)
	
	// 模拟执行任务
	time.Sleep(2 * time.Second)
	
	result := fmt.Sprintf("交换机上成功创建VRF: %s, 配置命令: ip vrf %s", req.VRFName, req.VRFName)
	log.Printf("[交换机任务] %s", result)
	
	return requestJSON, nil
}

// CreateVLANSubInterface 创建VLAN子接口
func CreateVLANSubInterface(requestJSON string) (string, error) {
	var req VPCRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[交换机任务] 开始创建VLAN子接口: VLAN %d (VPC: %s)", req.VLANId, req.VPCName)
	
	// 模拟执行任务
	time.Sleep(2 * time.Second)
	
	result := fmt.Sprintf("交换机上成功创建VLAN子接口: VLAN %d, 接口配置: interface Vlan%d, ip vrf forwarding %s", 
		req.VLANId, req.VLANId, req.VRFName)
	log.Printf("[交换机任务] %s", result)
	
	return requestJSON, nil
}

// CreateFirewallZone 创建防火墙安全区域
func CreateFirewallZone(requestJSON string) (string, error) {
	var req VPCRequest
	if err := json.Unmarshal([]byte(requestJSON), &req); err != nil {
		return "", fmt.Errorf("解析请求失败: %v", err)
	}

	log.Printf("[防火墙任务] 开始创建安全区域: %s (VPC: %s)", req.FirewallZone, req.VPCName)
	
	// 模拟执行任务
	time.Sleep(2 * time.Second)
	
	result := fmt.Sprintf("防火墙上成功创建安全区域: %s, 配置命令: security-zone name %s, set priority 100", 
		req.FirewallZone, req.FirewallZone)
	log.Printf("[防火墙任务] %s", result)
	
	return result, nil
}
