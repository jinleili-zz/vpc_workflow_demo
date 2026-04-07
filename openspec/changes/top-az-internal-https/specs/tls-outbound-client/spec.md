## ADDED Requirements

### Requirement: Top 侧统一 TLS Transport 构造
系统 SHALL 在 Top NSP 启动时，当 TLS 启用时，基于配置的 CA 证书路径构造一个共享的 `TransportProvider`（封装 `*http.Transport` 并支持运行时原子替换），并将该 provider 注入到所有 Top -> AZ 出站 HTTP client 中。

#### Scenario: TLS 启用时构造 TransportProvider
- **WHEN** Top NSP 启动且 `tls.enabled = true` 且 `tls.ca_cert_path` 指向有效的 CA 证书文件
- **THEN** 系统 MUST 加载 CA 证书到 RootCAs 信任池，创建 `TransportProvider`，并将其注入 `AZNSPClient`、`SignedTracedClient` 和 SAGA Executor

#### Scenario: TLS 未启用时保持默认行为
- **WHEN** Top NSP 启动且 `tls.enabled = false`
- **THEN** 系统 MUST 使用默认的 `*http.Transport`（无 TLS 配置），所有出站调用保持 HTTP 明文

#### Scenario: CA 证书文件不可读时启动失败
- **WHEN** Top NSP 启动且 `tls.enabled = true` 且 `tls.ca_cert_path` 指向不存在或不可读的文件
- **THEN** 系统 MUST 输出明确的错误日志并终止启动

### Requirement: 所有 Top -> AZ 出站调用统一使用共享 TransportProvider
系统 SHALL 确保 Top 侧所有到 AZ 的出站 HTTP 调用（包括 `AZNSPClient`、`SignedTracedClient`、SAGA Executor、以及原先散落的 `http.Get()`/`http.Post()` 调用）均通过同一个 `TransportProvider` 获取 Transport 后发起请求。Client 不得在构造时持有 `*http.Transport` 指针快照，MUST 在每次请求时调用 provider 获取当前 Transport。

#### Scenario: AZNSPClient 通过 provider 获取 Transport
- **WHEN** Top VPC orchestrator 通过 `AZNSPClient` 向 AZ 发起健康检查、状态轮询或 Subnet/PCCN 请求
- **THEN** 请求 MUST 通过调用 `TransportProvider` 获取当前 Transport 后发出

#### Scenario: SignedTracedClient 通过 provider 获取 Transport
- **WHEN** Top VFW PolicyService 或 Top API Server 通过 `SignedTracedClient` 向 AZ 发起请求
- **THEN** 请求 MUST 通过调用 `TransportProvider` 获取当前 Transport 后发出

#### Scenario: 消除散落的 stdlib 直接 HTTP 调用
- **WHEN** 代码中存在 `http.Get()` 或 `http.Post()` 直接调用 AZ 地址的位置（如 `CheckZonePolicies`）
- **THEN** 这些调用 MUST 被替换为使用统一 client 路径，通过共享 TLS Transport 发出

### Requirement: SAGA Executor TLS Transport 注入（前置依赖）
SAGA Executor 是 VPC/PCCN 创建/删除的唯一调用路径。系统 SHALL 在 `nsp_platform` 提供 `HTTPTransportProvider` 注入接口后，通过 bootstrap 将 `TransportProvider` 传入 SAGA Executor，使 SAGA 执行的 Top -> AZ 同步步骤使用与其他 client 相同的 TLS Transport。此能力是 AZ 侧切换 HTTPS 的阻塞前置条件。

#### Scenario: nsp_platform 提供注入接口后注入 TransportProvider
- **WHEN** `nsp_platform` SAGA 模块的 `ExecutorConfig` 包含 `HTTPTransportProvider` 字段，且 `tls.enabled = true`
- **THEN** bootstrap 的 `initSaga()` MUST 将与其他 client 相同的 `TransportProvider` 实例传入 SAGA Executor

#### Scenario: nsp_platform 接口就绪前不切换 AZ 到 HTTPS
- **WHEN** `nsp_platform` SAGA 模块尚未提供 `HTTPTransportProvider` 注入接口
- **THEN** 所有 AZ MUST 保持 `http://` 地址注册和 HTTP 监听，业务仓库可以完成 TLS 配置、TransportProvider 构造、AZ TLS 监听等准备工作并合入主干，但不得将 `tls.enabled` 设为 `true` 上线

### Requirement: Top 侧 CA 证书热更新
系统 SHALL 支持在不重启 Top NSP 进程的情况下更新 CA 信任链，以支持叶子证书续期和 CA 轮换。

#### Scenario: CA 文件更新后所有 client 的新请求使用新 CA
- **WHEN** Top NSP 运行中且 `tls.ca_cert_path` 指向的文件被替换为包含新 CA 的 bundle
- **THEN** 系统 MUST 在 `tls.ca_reload_interval` 时间内检测到文件变更，重建 `*http.Transport` 并通过 `TransportProvider` 内部原子替换，后续所有 client（包括 `AZNSPClient`、`SignedTracedClient`、SAGA Executor）的新请求 MUST 使用更新后的 CA 信任池

#### Scenario: CA 文件更新期间已有连接不中断
- **WHEN** CA 文件被替换且系统执行 Transport 原子替换
- **THEN** 旧 Transport 上已建立的活跃连接 MUST 继续正常完成，不被强制中断

#### Scenario: 双信任期支持 CA 轮换
- **WHEN** CA bundle 文件同时包含旧 CA 和新 CA
- **THEN** 系统 MUST 信任由任一 CA 签发的 AZ 证书，支持渐进式 CA 轮换
