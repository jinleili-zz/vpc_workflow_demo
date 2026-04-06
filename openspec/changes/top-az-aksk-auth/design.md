## Context

### 背景

NSP系统采用多地域分布式部署架构，Top-NSP和AZ-NSP位于不同地域的Kubernetes集群中。跨地域的HTTP通信需要身份认证，防止未授权访问。

### 当前状态

- Top-NSP通过`AZNSPClient`和Saga引擎调用AZ-NSP的HTTP API
- AZ-NSP接收请求后直接处理，无任何认证机制
- `nsp-platform/auth`已提供完整的AK/SK签名和验证能力
- `nsp-platform/saga`已支持`Step.AuthAK`和`Engine.Config.CredentialStore`

### 约束

- AK/SK以明文形式存储在配置文件中（生产环境可考虑Vault等密钥管理）
- 全局使用一套AK/SK凭证
- AZ-NSP和Worker之间的Redis队列通信不涉及HTTP，不受影响

## Goals / Non-Goals

**Goals:**
- Top-NSP和AZ-NSP启动时从配置文件加载AK/SK
- AZ-NSP验证所有业务API请求的签名（健康检查除外）
- Top-NSP对所有发往AZ-NSP的HTTP请求签名
- Saga引擎执行的HTTP请求支持签名

**Non-Goals:**
- 不涉及外部调用者对Top-NSP的认证
- 不涉及AZ-NSP向Top-NSP注册/心跳的签名
- 不涉及Worker与AZ-NSP之间的Redis队列通信
- 不涉及AK/SK的动态轮换机制

## Decisions

### D1: AK/SK配置方式

**决定**: 使用YAML配置文件，明文存储AK/SK

**配置文件结构**:
```yaml
# config.yaml
auth:
  credentials:
    - access_key: "top-nsp"
      secret_key: "${TOP_NSP_SK}"  # 支持环境变量引用
      label: "Top NSP"
      enabled: true
```

**备选方案**:
1. 环境变量传递 - 不选，多服务配置复杂
2. Vault/Secrets Manager - 暂不采用，增加部署复杂度
3. 数据库存储 - 不选，启动时依赖数据库不合理

**理由**: 配置文件方式简单直观，支持环境变量注入敏感信息，后续可平滑迁移到Vault。

### D2: 签名范围

**决定**: 仅对Top→AZ的业务API签名，AZ→Top的注册/心跳免签名

**理由**:
- AZ向Top注册/心跳走内网，可依赖网络层安全
- 业务API涉及跨地域调用，必须认证
- 简化AZ-NSP实现，无需在注册逻辑中集成签名

**免认证路径**:
- `/api/v1/health` - 健康检查
- `/api/v1/register/az` - AZ注册（Top-NSP接收）
- `/api/v1/heartbeat` - 心跳（Top-NSP接收）

### D3: AZNSPClient改造方式

**决定**: 在`AZNSPClient`结构体中添加`signer *auth.Signer`字段，统一为所有方法签名

**改造后的结构**:
```go
type AZNSPClient struct {
    httpClient   *http.Client
    tracedClient *trace.TracedClient
    signer       *auth.Signer  // 新增
}

func NewAZNSPClient(signer *auth.Signer) *AZNSPClient { ... }
func NewAZNSPClientWithTrace(tracedClient *trace.TracedClient, signer *auth.Signer) *AZNSPClient { ... }
```

**理由**:
- 集中管理签名逻辑，避免遗漏
- 兼容现有调用方式，仅修改构造函数

### D4: Saga签名实现

**决定**: 利用Saga引擎已有的`Step.AuthAK`和`Config.CredentialStore`能力

**实现方式**:
1. Bootstrap初始化Saga时传入`CredentialStore`
2. 构建Saga Step时设置`AuthAK: "top-nsp"`

**代码示例**:
```go
// bootstrap.go - initSaga()
sagaCfg := &saga.Config{
    DSN:            cfg.PostgresDSN,
    WorkerCount:    cfg.SagaWorkerCount,
    InstanceID:     cfg.InstanceID,
    CredentialStore: credStore,  // 新增
}

// orchestrator.go - CreateRegionVPC()
builder.AddStep(saga.Step{
    Name:             fmt.Sprintf("创建VPC-%s", az.ID),
    Type:             saga.StepTypeSync,
    ActionMethod:     "POST",
    ActionURL:        fmt.Sprintf("%s/api/v1/vpc", az.NSPAddr),
    ActionPayload:    payloadMap,
    CompensateMethod: "DELETE",
    CompensateURL:    fmt.Sprintf("%s/api/v1/vpc/%s", az.NSPAddr, req.VPCName),
    AuthAK:           "top-nsp",  // 新增
})
```

**理由**: 无需修改nsp-platform/saga，利用现有能力即可实现。

### D5: 直接HTTP调用改造

**决定**: 统一封装`SignedTracedClient`，为`internal/top/api/server.go`和`internal/top/vfw/service/policy.go`中的直接HTTP调用提供签名能力

**方案**: 扩展`trace.TracedClient`或创建包装器，自动为请求签名

**理由**:
- 当前`api/server.go`中直接使用`s.tracedHTTP.Get/Do`
- 当前`vfw/service/policy.go`中直接使用`http.Post`
- 需要统一封装，避免重复代码

## Risks / Trade-offs

### R1: AK/SK泄露风险

**风险**: 配置文件明文存储AK/SK，存在泄露风险

**缓解**:
- 使用环境变量注入敏感值：`secret_key: "${TOP_NSP_SK}"`
- Kubernetes Secret管理配置文件
- 后续可迁移到Vault

### R2: 时钟不同步导致签名验证失败

**风险**: 签名包含时间戳，服务器时钟不同步可能导致验证失败

**缓解**:
- auth模块已支持可配置的时间窗口（默认5分钟）
- 生产环境部署NTP同步

### R3: 改造遗漏导致调用失败

**风险**: 某些HTTP调用点遗漏签名改造，导致AZ-NSP返回401

**缓解**:
- 提案已完整梳理所有HTTP交互点
- 集成测试覆盖所有调用路径
- 分阶段上线：先AZ免认证，验证签名逻辑后开启认证

## Migration Plan

### 阶段1: 代码改造（不启用认证）

1. 改造配置加载，支持AK/SK配置
2. 改造AZNSPClient，添加Signer
3. 改造Saga，传入CredentialStore
4. AZ-NSP添加中间件，但配置`EnableAuth: false`

### 阶段2: 验证签名逻辑

1. 启用AZ-NSP认证（`EnableAuth: true`）
2. 运行集成测试，验证所有调用路径
3. 监控日志，确认无401错误

### 阶段3: 生产部署

1. 配置生产环境AK/SK（通过Kubernetes Secret）
2. 滚动更新AZ-NSP
3. 滚动更新Top-NSP

### 回滚策略

- 将`EnableAuth`设为`false`即可禁用认证
- 配置文件回滚即可恢复

## Open Questions

1. **TracedClient签名集成方式**: 是扩展`trace.TracedClient`还是创建新的`SignedClient`包装器？
   - 建议：创建包装器`SignedTracedClient`，组合`TracedClient`和`Signer`

2. **多个AK/SK支持**: 当前设计仅支持一套AK/SK，未来是否需要支持多租户场景？
   - 当前决定：单AK/SK，后续需要时再扩展
