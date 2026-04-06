## ADDED Requirements

### Requirement: AK/SK认证中间件

AZ-NSP SHALL对所有入站HTTP请求进行AK/SK签名验证（免认证路径除外）。

#### Scenario: 有效签名请求通过验证
- **WHEN** 请求包含有效的Authorization头和签名
- **THEN** 请求通过验证并继续处理

#### Scenario: 无签名请求被拒绝
- **WHEN** 请求缺少Authorization头或签名无效
- **THEN** 系统返回HTTP 401 Unauthorized

#### Scenario: 签名过期请求被拒绝
- **WHEN** 请求签名中的时间戳超出有效窗口（默认5分钟）
- **THEN** 系统返回HTTP 401 Unauthorized

### Requirement: 免认证路径配置

AZ-NSP SHALL支持配置免认证路径。

#### Scenario: 健康检查免认证
- **WHEN** 请求路径为`/api/v1/health`
- **THEN** 请求跳过签名验证

#### Scenario: 自定义免认证路径
- **WHEN** 配置文件指定skip_auth_paths
- **THEN** 匹配路径的请求跳过签名验证

### Requirement: 凭证验证

AZ-NSP SHALL验证请求中的AccessKey是否存在于信任列表中。

#### Scenario: 信任的AccessKey
- **WHEN** 请求的AccessKey存在于配置的credentials列表中且enabled为true
- **THEN** 使用对应的SecretKey验证签名

#### Scenario: 未信任的AccessKey
- **WHEN** 请求的AccessKey不存在于credentials列表中
- **THEN** 系统返回HTTP 401 Unauthorized
