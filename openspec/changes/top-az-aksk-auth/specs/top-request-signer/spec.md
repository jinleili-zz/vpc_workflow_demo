## ADDED Requirements

### Requirement: HTTP客户端签名能力

Top-NSP的HTTP客户端SHALL为所有发往AZ-NSP的请求自动添加AK/SK签名。

#### Scenario: AZNSPClient自动签名
- **WHEN** AZNSPClient发起HTTP请求
- **THEN** 请求自动添加Authorization头和签名

#### Scenario: TracedClient签名集成
- **WHEN** 使用TracedClient发起请求
- **THEN** 请求同时支持链路追踪和AK/SK签名

### Requirement: 签名格式

Top-NSP SHALL使用NSP-HMAC-SHA256签名算法。

#### Scenario: 签名头格式
- **WHEN** 请求被签名
- **THEN** 请求头包含Authorization、X-Auth-Timestamp、X-Auth-Nonce等字段

#### Scenario: 签名内容
- **WHEN** 计算签名
- **THEN** 签名涵盖HTTP方法、URL路径、查询参数、请求体和时间戳

### Requirement: 所有交互点签名

Top-NSP SHALL为以下所有HTTP交互点添加签名：

#### Scenario: AZNSPClient方法签名
- **WHEN** 调用HealthCheck、CreateVPC、DeleteVPC、GetVPCStatus、CreateSubnet、GetPCCNStatus、DeletePCCN等方法
- **THEN** 请求包含有效签名

#### Scenario: 直接HTTP调用签名
- **WHEN** top/api/server.go中直接调用GetSubnetStatus、DeleteSubnet
- **THEN** 请求包含有效签名

#### Scenario: VFW服务调用签名
- **WHEN** vfw/service/policy.go中调用AZ防火墙策略API
- **THEN** 请求包含有效签名
