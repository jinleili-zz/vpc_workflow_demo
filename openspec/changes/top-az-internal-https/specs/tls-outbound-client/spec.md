## ADDED Requirements

### Requirement: Top VPC 侧统一 mTLS reloadableTransport 构造
系统 SHALL 在 Top VPC NSP 启动时，当 TLS 启用时，基于配置的 CA 证书路径和客户端证书/私钥路径构造一个 `reloadableTransport`（实现 `http.RoundTripper` 接口，内部通过 `atomic.Value` 持有 `*http.Transport` 并支持运行时原子替换），创建共享的 `*http.Client{Transport: reloadableTransport}` 并将其注入到所有 Top VPC -> AZ VPC 出站 HTTP client 中。Top VFW NSP 不使用此机制。

#### Scenario: TLS 启用时构造 reloadableTransport 和共享 HTTPClient
- **WHEN** Top VPC NSP 启动且 `tls.enabled = true` 且 `tls.ca_cert_path` 指向有效的 CA 证书文件且 `tls.cert_path`/`tls.key_path` 指向有效的客户端证书和私钥
- **THEN** 系统 MUST 加载 CA 证书到 RootCAs 信任池、加载客户端证书到 Certificates，创建 `reloadableTransport`，构造共享 `*http.Client`，并将其注入 `AZNSPClient` 和 SAGA Engine（`saga.Config.HTTPClient`）

#### Scenario: TLS 未启用时保持默认行为
- **WHEN** Top VPC NSP 启动且 `tls.enabled = false`
- **THEN** 系统 MUST 使用默认的 `*http.Transport`（无 TLS 配置），所有 VPC 出站调用保持 HTTP 明文

#### Scenario: CA 证书文件不可读时启动失败
- **WHEN** Top VPC NSP 启动且 `tls.enabled = true` 且 `tls.ca_cert_path` 指向不存在或不可读的文件
- **THEN** 系统 MUST 输出明确的错误日志并终止启动

#### Scenario: 客户端证书文件不可读时启动失败
- **WHEN** Top VPC NSP 启动且 `tls.enabled = true` 且 `tls.cert_path` 或 `tls.key_path` 指向不存在或不可读的文件
- **THEN** 系统 MUST 输出明确的错误日志并终止启动

### Requirement: 所有 Top VPC -> AZ VPC 出站调用统一使用共享 mTLS HTTPClient
系统 SHALL 确保 Top VPC 侧所有到 AZ VPC 的出站 HTTP 调用（包括 `AZNSPClient`、SAGA Engine、以及 Top API Server 中 Subnet/PCCN 操作）均通过同一个 `*http.Client`（Transport 为 `reloadableTransport`）发起请求。Top VFW 侧调用不受影响，继续使用 `SignedTracedClient` + AK/SK。

#### Scenario: AZNSPClient 使用共享 mTLS HTTPClient
- **WHEN** Top VPC orchestrator 通过 `AZNSPClient` 向 AZ VPC 发起健康检查、状态轮询或 Subnet/PCCN 请求
- **THEN** 请求 MUST 通过共享的 `*http.Client`（底层 `reloadableTransport`）发出

### Requirement: SAGA Engine mTLS 适配（利用已有 HTTPClient 注入接口）
SAGA Engine 是 VPC/PCCN 创建/删除的唯一调用路径。系统 SHALL 在 bootstrap 时将带 mTLS 的共享 `*http.Client` 通过 `saga.Config.HTTPClient` 传入 SAGA Engine，使 SAGA 执行的 Top -> AZ 同步步骤使用与其他 client 相同的 mTLS Transport。`nsp-platform` 已提供此注入接口，无需平台侧额外改造。

#### Scenario: bootstrap 时注入 mTLS HTTPClient 到 SAGA
- **WHEN** `tls.enabled = true` 且 Top NSP 初始化 SAGA Engine
- **THEN** bootstrap 的 `initSaga()` MUST 将与其他 client 相同的共享 `*http.Client`（底层 `reloadableTransport`）传入 `saga.Config.HTTPClient`

#### Scenario: TLS 未启用时 SAGA 保持默认行为
- **WHEN** `tls.enabled = false`
- **THEN** `saga.Config.HTTPClient` MUST 保持 nil，SAGA Engine 使用内部自建的 plain `*http.Client`

### Requirement: Top VPC 侧 CA 和客户端证书热更新
系统 SHALL 支持在不重启 Top VPC NSP 进程的情况下更新 CA 信任链和客户端证书，以支持叶子证书续期和 CA 轮换。

#### Scenario: CA 或客户端证书文件更新后所有 VPC client 的新请求使用新证书
- **WHEN** Top VPC NSP 运行中且 `tls.ca_cert_path`、`tls.cert_path` 或 `tls.key_path` 指向的文件被替换
- **THEN** 系统 MUST 在 `tls.ca_reload_interval` 时间内检测到文件变更，重建内部 `*http.Transport` 并通过 `reloadableTransport` 内部 `atomic.Value` 原子替换，后续所有 VPC client（包括 `AZNSPClient`、SAGA Engine）的新请求 MUST 使用更新后的 CA 信任池和客户端证书

#### Scenario: CA 文件更新期间已有连接不中断
- **WHEN** CA 文件被替换且系统执行 Transport 原子替换
- **THEN** 旧 Transport 上已建立的活跃连接 MUST 继续正常完成，不被强制中断

#### Scenario: 双信任期支持 CA 轮换
- **WHEN** CA bundle 文件同时包含旧 CA 和新 CA
- **THEN** 系统 MUST 信任由任一 CA 签发的 AZ 证书，支持渐进式 CA 轮换
