## Why

当前 Top VPC NSP 与 AZ VPC NSP 之间的 VPC/Subnet/PCCN 调用使用 AK/SK 签名 + HTTP 明文传输。虽然 AK/SK 可防止请求伪造，但 HTTP 明文仍暴露了请求/响应体中的敏感业务数据（VPC CIDR、子网配置等），存在被中间人窃听或篡改的风险。

本次改造将 VPC 路径（VPC/Subnet/PCCN）的安全模型从 AK/SK 切换为 mTLS（双向证书认证），Top 验证 AZ VPC 服务端身份，AZ VPC 同时验证 Top 客户端身份，形成传输加密 + 双向身份认证的安全模型。mTLS 替代 AK/SK 而非叠加使用。

VFW 路径继续使用 AK/SK + HTTP，不在本次改造范围内。

当前实现存在以下具体缺口：

1. **VPC HTTP client 分裂**：Top -> AZ VPC 调用分散在 `AZNSPClient`、SAGA `Executor` 内部 `*http.Client`、以及 Top API Server 中的 Subnet/PCCN 操作中，没有统一的 TLS Transport 注入点。
2. **SAGA 引擎 client 独立**：SAGA 引擎（`nsp-platform` 模块）已提供 `HTTPClient *http.Client` 注入接口（`saga.Config.HTTPClient` 和 `saga.ExecutorConfig.HTTPClient`），但业务仓库当前未注入自定义 client，使用引擎内部自建的 plain `*http.Client`，无 TLS 配置。
3. **AZ VPC 地址默认 http**：AZ VPC 自注册时硬编码 `http://` scheme，Top 侧 registry 存储的地址也全部是 `http://` 前缀。
4. **证书生命周期未建模**：代码中不存在任何 TLS 配置项、证书文件路径、CA bundle 加载、或证书热更新机制。

## What Changes

- **统一 Top VPC -> AZ VPC 出站 mTLS Transport**：在业务仓库中构造一个共享的、配置了 CA 信任链和客户端证书的 `reloadableTransport`（实现 `http.RoundTripper`，内部 `atomic.Value` 支持热更新），创建统一的 `*http.Client` 供 `AZNSPClient`、SAGA Engine、以及 Top API Server 中 Subnet/PCCN 操作复用。
- **SAGA client 注入**：利用 `nsp_platform` SAGA 模块已有的 `HTTPClient *http.Client` 注入接口，直接将带 mTLS 的 `*http.Client` 传入 `saga.Config.HTTPClient`，无需平台侧额外改造。
- **AZ VPC 地址 scheme 升级**：AZ VPC 自注册时上报 `https://` 地址（进程内 TLS 终止模式）或由 LB/Ingress 终止 TLS 后上报对应 HTTPS 入口地址。Top 侧 registry 存储完整的 `https://` URL。
- **配置项扩展**：在 `NSPConfig` 中增加 TLS 相关配置段，包括 CA 证书路径、是否启用 TLS、证书/密钥路径（AZ 侧）。
- **证书热更新能力**：业务仓库实现基于文件变更监听或定时轮询的证书 reload 机制，支持叶子证书续期和 CA 轮换场景下的平滑切换。
- **消除散落的 `http.Get()`/`http.Post()` 调用**：将 Top 侧散落的直接 stdlib HTTP 调用统一收口到带 TLS 的 client 路径。

## Capabilities

### New Capabilities
- `tls-outbound-client`: Top VPC 侧统一的 mTLS 出站 client 构造与管理，包括 CA 信任链加载、客户端证书加载、reloadableTransport (RoundTripper wrapper)、证书热更新。
- `az-tls-endpoint`: AZ VPC 侧 mTLS 终点暴露，包括 TLS 监听配置、服务端证书加载、客户端证书验证（ClientAuth）、地址上报 scheme 升级。
- `tls-config`: TLS 相关配置项定义与加载，包括 CA 路径、证书路径、客户端认证开关、启用开关、reload 策略。仅 VPC 服务使用。

### Modified Capabilities
<!-- 无现有 spec 需要修改 -->

## Impact

**受影响代码：**
- `internal/client/az_client.go` - AZNSPClient 需接受外部注入的 `*http.Client`
- `internal/config/config.go` - 增加 TLS 配置段
- `internal/bootstrap/bootstrap.go` - 初始化 mTLS reloadableTransport、构造共享 `*http.Client` 并注入 AZNSPClient 和 SAGA
- `internal/top/orchestrator/orchestrator.go` - 适配 mTLS client
- `internal/az/api/server.go` - AZ VPC 自注册地址 scheme 升级、mTLS 监听（含 ClientAuth）
- `config/config.yaml` - 增加 TLS 配置段
- `deployments/docker/` - 证书生成脚本（openssl）、证书挂载、环境变量

**外部依赖：**
- `nsp_platform` SAGA 模块已提供 `HTTPClient *http.Client` 注入接口，无需额外改造

**不受影响：**
- Top VFW NSP 及 AZ VFW NSP（继续使用 AK/SK + HTTP）
- `internal/client/signed_traced_client.go` - VFW 专用，不引入 TLS
- `internal/az/vfw/api/server.go` - VFW AZ 继续 HTTP 监听
- Top NSP 对外 HTTP API（不改为 HTTPS）
- AZ -> Top 注册/心跳链路（不纳入本次 HTTPS 改造）
- Redis/PostgreSQL 连接（不在本次范围）
