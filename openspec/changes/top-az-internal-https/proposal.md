## Why

当前 Top NSP 与 AZ NSP 之间的所有内部调用（VPC/Subnet/PCCN/VFW）均使用明文 HTTP，缺乏传输层加密。虽然 AK/SK 签名机制已经存在并可防止请求伪造，但 HTTP 明文传输仍然暴露了请求/响应体中的敏感业务数据（VPC CIDR、子网配置、防火墙策略规则等），存在被中间人窃听或篡改的风险。本次改造仅针对 Top -> AZ 这条内部出站链路引入 HTTPS，与 AK/SK 机制叠加使用，形成"传输加密 + 请求签名"的双层安全模型。

当前实现存在以下具体缺口：

1. **HTTP client 分裂**：Top -> AZ 调用分散在至少四个独立 client 路径中（`AZNSPClient`、`SignedTracedClient`、SAGA `Executor` 内部 `*http.Client`、以及散落的 `http.Get()`/`http.Post()`），没有统一的 TLS Transport 注入点。
2. **SAGA 引擎 client 独立**：SAGA 引擎（`nsp-platform` 模块）内部自建 `*http.Client`，业务仓库无法直接控制其 TLS 配置，依赖平台侧提供 client 注入能力。
3. **AZ 地址默认 http**：AZ 自注册时硬编码 `http://` scheme，Top 侧 registry 存储的地址也全部是 `http://` 前缀。
4. **证书生命周期未建模**：代码中不存在任何 TLS 配置项、证书文件路径、CA bundle 加载、或证书热更新机制。

## What Changes

- **统一 Top -> AZ 出站 TLS Transport**：在业务仓库中构造一个共享的、配置了 CA 信任链的 `*http.Transport`，供 `AZNSPClient`、`SignedTracedClient`、以及散落的直接 HTTP 调用复用。
- **SAGA client 注入**：依赖 `nsp_platform` SAGA 模块提供自定义 `*http.Client` 或 `*http.Transport` 注入接口，业务仓库通过该接口将 TLS Transport 传入 SAGA `Executor`。
- **AZ 地址 scheme 升级**：AZ 自注册时上报 `https://` 地址（进程内 TLS 终止模式）或由 LB/Ingress 终止 TLS 后上报对应 HTTPS 入口地址。Top 侧 registry 存储完整的 `https://` URL。
- **配置项扩展**：在 `NSPConfig` 中增加 TLS 相关配置段，包括 CA 证书路径、是否启用 TLS、证书/密钥路径（AZ 侧）。
- **证书热更新能力**：业务仓库实现基于文件变更监听或定时轮询的证书 reload 机制，支持叶子证书续期和 CA 轮换场景下的平滑切换。
- **消除散落的 `http.Get()`/`http.Post()` 调用**：将 Top 侧散落的直接 stdlib HTTP 调用统一收口到带 TLS 的 client 路径。

## Capabilities

### New Capabilities
- `tls-outbound-client`: Top 侧统一的 TLS 出站 client 构造与管理，包括 CA 信任链加载、Transport 共享、证书热更新。
- `az-tls-endpoint`: AZ 侧 HTTPS 终点暴露，包括 TLS 监听配置、证书加载、地址上报 scheme 升级。
- `tls-config`: TLS 相关配置项定义与加载，包括 CA 路径、证书路径、启用开关、reload 策略。

### Modified Capabilities
<!-- 无现有 spec 需要修改 -->

## Impact

**受影响代码：**
- `internal/client/az_client.go` - AZNSPClient 需接受外部注入的 `*http.Transport`
- `internal/client/signed_traced_client.go` - SignedTracedClient 需接受外部注入的 `*http.Transport`
- `internal/config/config.go` - 增加 TLS 配置段
- `internal/bootstrap/bootstrap.go` - 初始化 TLS Transport 并注入各 client
- `internal/top/orchestrator/orchestrator.go` - 消除散落的 `http.Get()` 调用（如 `CheckZonePolicies`）
- `internal/top/vfw/service/policy.go` - SignedTracedClient 实例化适配
- `internal/az/api/server.go` - AZ 自注册地址 scheme 升级、可选 TLS 监听
- `internal/az/vfw/api/server.go` - AZ VFW 自注册地址 scheme 升级、可选 TLS 监听
- `config/config.yaml` - 增加 TLS 配置段
- `deployments/docker/` - 证书挂载、环境变量

**外部依赖：**
- `nsp_platform` SAGA 模块需提供 `*http.Client` 或 `*http.Transport` 注入接口（当前 `saga.Executor` 内部自建 client，业务仓库无法控制）

**不受影响：**
- Top NSP 对外 HTTP API（不改为 HTTPS）
- AZ -> Top 注册/心跳链路（不纳入本次 HTTPS 改造）
- AK/SK 签名机制（保持不变，与 HTTPS 叠加使用）
- Redis/PostgreSQL 连接（不在本次范围）
