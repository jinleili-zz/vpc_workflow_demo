## Context

当前 Top NSP（VPC + VFW）与 AZ NSP（VPC + VFW）之间的所有内部 HTTP 调用均为明文传输。调用链涉及四条独立的 HTTP client 路径：

1. **AZNSPClient** (`internal/client/az_client.go`) - Top VPC orchestrator 用于健康检查、状态轮询、Subnet/PCCN 直接调用
2. **SignedTracedClient** (`internal/client/signed_traced_client.go`) - Top VFW PolicyService 和 Top API Server 用于 VFW 策略下发、Subnet 状态/删除
3. **SAGA Executor** (`nsp-platform/saga/executor.go`) - SAGA 引擎内部自建 `*http.Client`，用于 VPC/PCCN 创建/删除的同步步骤执行
4. **散落的 `http.Get()`/`http.Post()`** - `orchestrator.CheckZonePolicies()` 等位置使用 stdlib 默认 client

AK/SK 签名机制已覆盖路径 1、2、3（SAGA 通过 `credStore` 按步骤签名），但路径 4 完全绕过签名和 tracing。

AZ 自注册时硬编码 `http://` scheme（`internal/az/api/server.go:551`、`internal/az/vfw/api/server.go:252`），Top registry 存储的地址也是 `http://` 前缀。

## Goals / Non-Goals

**Goals:**
- 为 Top -> AZ 内部出站链路提供传输层加密（TLS），与现有 AK/SK 叠加形成双层安全
- 统一 Top 侧所有 Top -> AZ 出站调用的 TLS Transport，消除 client 路径分裂
- 支持 SAGA 引擎使用业务仓库提供的 TLS Transport（依赖平台侧注入接口）
- 支持证书热更新：叶子证书续期和 CA 轮换场景下不中断服务
- AZ 地址 scheme 从 `http://` 升级为 `https://`
- 明确 `vpc_workflow_demo`（业务仓库）与 `nsp_platform`（平台仓库）的职责边界

**Non-Goals:**
- Top 对外 API HTTPS（Top 对外入口不改）
- AZ -> Top 注册/心跳 HTTPS（不纳入实施范围）
- mTLS（双向证书认证；本次只做单向 TLS，Top 验证 AZ 证书）
- AK/SK 协议重构（保持不变）
- nonce store / credential store 改造
- Redis / PostgreSQL 连接加密

## Decisions

### Decision 1: TLS 终止方式 - AZ 进程内终止 vs 前置 LB/Ingress

**方案 A：AZ 进程内终止 TLS（推荐）**

AZ NSP 进程直接使用 Go 标准库 `tls.Listen()` / `http.Server.ServeTLS()` 监听 HTTPS 端口。

优点：
- 端到端加密，无中间明文段
- 部署简单，无额外基础设施依赖
- 与当前 Docker 单进程部署模型一致

缺点：
- 每个 AZ 进程需管理自己的证书文件
- 证书更新需进程侧实现 reload

**方案 B：前置 LB/Ingress 终止 TLS**

在 AZ 进程前部署 Nginx/Envoy/云 LB 做 TLS 卸载，AZ 进程仍监听 HTTP。

优点：
- AZ 进程无需改动监听逻辑
- 证书管理集中在 LB 层

缺点：
- LB 到 AZ 进程之间仍是明文（除非再做一层加密）
- 增加基础设施复杂度
- 不适合当前 Docker Compose 部署模型

**决策：选择方案 A（AZ 进程内终止 TLS）。** 理由：当前系统以 Docker Compose 部署为主，进程内终止 TLS 更简单直接。同时在配置中预留 `tls.mode` 字段（值为 `process` 或 `lb`），方便未来切换到 LB 终止模式——切换时 AZ 进程只需关闭 TLS 监听并将上报地址指向 LB 的 HTTPS 入口即可。

### Decision 2: Top 侧统一 TLS Transport 构造（可重载）

在 `internal/bootstrap/bootstrap.go` 中新增 TLS Transport 初始化逻辑：

- 读取配置中的 CA 证书路径，构造 `*tls.Config` 并设置 `RootCAs`
- 基于该 `*tls.Config` 创建 `*http.Transport`
- 引入 `TransportProvider` 函数类型（`type TransportProvider func() *http.Transport`），内部通过 `atomic.Value` 管理当前 Transport 指针
- 将 `TransportProvider` 注入到所有 Top -> AZ 出站 client：
  - `AZNSPClient` - 构造时接收 `TransportProvider`，每次 `do()` 请求时调用 provider 获取最新 Transport
  - `SignedTracedClient` / `TracedClient` - 同上，每次请求时通过 provider 获取当前 Transport
  - 散落的 `http.Get()`/`http.Post()` - 替换为使用统一 client 的调用
  - SAGA Executor - 通过 `saga.ExecutorConfig.HTTPTransportProvider` 传入同一个 provider（见 Decision 3）

**关键设计点**：client 不持有 `*http.Transport` 指针的快照，而是持有 `TransportProvider` 回调。CA 文件变更时，bootstrap 的后台 goroutine 重建 Transport 并原子更新到 provider 内部，所有 client（包括 SAGA Executor）的后续请求自动使用新 Transport，无需重建 client 实例。

**不新建独立 TLS 工具包**。TLS Transport 构造和 provider 逻辑直接在 bootstrap 中实现，作为初始化步骤之一。如果未来需要在更多场景复用，再提取为独立 package。

### Decision 3: SAGA 引擎 TLS 适配 - 职责边界与前置依赖

**SAGA 是 Top -> AZ 主链路的硬依赖。** VPC/PCCN 的创建和删除通过 SAGA sync step 调用 AZ，这是核心业务流量。一旦 AZ 侧切换到 `https://` 地址，SAGA Executor 的 `*http.Client` 必须具备自定义 RootCAs 才能完成 TLS 握手。因此 **nsp_platform 侧提供 Transport 注入能力是整个 HTTPS 改造的前置条件**，不能作为独立的后续阶段。

**平台仓库（`nsp_platform`）职责：**
- 在 `saga.ExecutorConfig` 中新增 `HTTPTransportProvider func() *http.Transport` 字段（函数类型，支持动态获取 Transport）
- `NewExecutor()` 中：若 `HTTPTransportProvider` 非 nil，Executor 内部 `*http.Client` 的每次请求通过 provider 获取当前 Transport；若为 nil，保持当前行为（自建 plain `*http.Client`）
- 平台侧不负责 TLS 配置细节、CA 加载、证书管理

**业务仓库（`vpc_workflow_demo`）职责：**
- 构造 `TransportProvider`（与 Decision 2 相同的 provider 实例）
- 在 `internal/bootstrap/bootstrap.go` 的 `initSaga()` 中将 provider 传入 `saga.ExecutorConfig.HTTPTransportProvider`
- 业务仓库必须等待 `nsp_platform` 模块发布包含该字段的版本后才能编译集成

**本仓库不修改 `nsp_platform` 代码。** 但 SAGA 注入接口是本仓库 HTTPS 改造上线的阻塞依赖，而非可选的后续增强。实施顺序为：先推动平台侧发布接口 -> 业务仓库 `go mod` 升级依赖 -> 统一注入 provider -> AZ 侧切换 HTTPS。

### Decision 4: AZ 地址 scheme 升级

AZ 自注册时根据 TLS 配置和模式决定上报地址的 scheme：
- `tls.mode = "process"` 且 TLS 启用时：`https://az-nsp-{az}:{port}`
- `tls.mode = "lb"` 时：**必须**通过 `NSP_ADDR` / `NSP_VFW_ADDR` 环境变量显式指定地址（因为 AZ 进程本身不监听 TLS，无法自动推断正确的 HTTPS 入口）；若环境变量未设置，回退到 `http://az-nsp-{az}:{port}`（HTTP 明文），并输出警告日志
- TLS 未启用时：保持 `http://az-nsp-{az}:{port}`

地址 scheme 由 AZ 侧决定（AZ 知道自己是否启用了 TLS），Top 侧不做 scheme 推断或覆盖。Top 侧的 TLS client 需同时支持 http 和 https 目标（渐进式迁移期间可能混合存在）。

### Decision 5: 证书热更新策略

**叶子证书续期（CA 不变）：**
1. 使用 `tls.Config.GetCertificate` 回调（AZ 侧），每次 TLS 握手时从文件动态加载证书
2. 证书文件被替换后，新连接自动使用新证书，已建立的连接不受影响
3. 无需 SIGHUP 或重启

**CA 轮换：**
1. Top 侧的 `RootCAs` 需要支持动态更新
2. 实现方式：启动一个后台 goroutine，定期（如 5 分钟）检查 CA 文件修改时间，若变化则重建 `*tls.Config` 并原子替换 `*http.Transport`
3. CA 轮换顺序：
   - 第一步：在 Top 侧 CA bundle 中同时包含旧 CA 和新 CA（双信任期）
   - 第二步：AZ 侧切换到新 CA 签发的证书
   - 第三步：从 Top 侧 CA bundle 中移除旧 CA
4. 双信任期确保切换过程中不中断

**Transport 动态获取机制：**
- 使用 `atomic.Value` 在 `TransportProvider` 内部存储当前 `*http.Transport` 指针
- 所有 client（`AZNSPClient`、`SignedTracedClient`、SAGA Executor）持有 `TransportProvider` 回调而非 `*http.Transport` 指针
- 每次发起请求时通过 `provider()` 调用获取最新 Transport，确保 CA 更新后的新 Transport 立即生效
- 旧 Transport 上的活跃连接自然结束后由 GC 回收

### Decision 6: 配置项设计

在 `NSPConfig` 中新增 `TLS` 配置段：

```yaml
tls:
  enabled: false                    # 总开关
  mode: "process"                   # "process" (进程内终止) 或 "lb" (LB 终止)
  ca_cert_path: ""                  # Top 侧：CA 证书路径（用于验证 AZ 证书）
  cert_path: ""                     # AZ 侧：服务端证书路径
  key_path: ""                      # AZ 侧：服务端私钥路径
  ca_reload_interval: "5m"          # CA 文件变更检查间隔
  insecure_skip_verify: false       # 仅用于测试环境
```

环境变量覆盖（遵循 `NSP_` 前缀约定）：
- `NSP_TLS_ENABLED`
- `NSP_TLS_MODE`
- `NSP_TLS_CA_CERT_PATH`
- `NSP_TLS_CERT_PATH`
- `NSP_TLS_KEY_PATH`

## Risks / Trade-offs

**[风险] SAGA 平台侧接口未就绪** → 缓解：SAGA 是 VPC/PCCN 主链路的唯一调用路径，因此 `nsp_platform` 的 `HTTPTransportProvider` 注入接口是整个 HTTPS 改造上线的阻塞依赖。业务仓库在平台接口就绪前可以完成 TLS 配置、TransportProvider 构造、AZ 侧 TLS 监听等所有准备工作并合入主干，但 **不能将任何 AZ 的注册地址切换到 `https://`**。实际切换 HTTPS 需等平台侧发布后统一进行。

**[风险] 证书文件挂载错误导致启动失败** → 缓解：启动时校验证书文件可读性和有效性（到期时间检查），失败时输出明确错误日志。`tls.enabled = false` 时完全跳过 TLS 初始化，保持向后兼容。

**[风险] 渐进式迁移期间 http/https 混合** → 缓解：Top 侧 TLS Transport 配置为可选（遇到 `http://` 目标时不强制 TLS）。AZ 地址 scheme 由 AZ 自行上报，Top 按实际 scheme 发起请求。

**[风险] CA 轮换时序错误导致连接中断** → 缓解：文档化严格的三步轮换流程（双信任期 -> 切换证书 -> 移除旧 CA）。Top 侧 CA reload 间隔默认 5 分钟，确保新 CA 文件部署后能被及时加载。

**[Trade-off] 进程内 TLS vs LB 终止** → 选择进程内 TLS 增加了每个 AZ 进程的证书管理负担，但避免了引入额外基础设施。通过 `tls.mode` 配置项保留未来切换到 LB 模式的能力。

**[Trade-off] `atomic.Value` + TransportProvider 间接引用** → 每次 HTTP 请求多一次函数调用 + atomic.Load 开销（纳秒级），换来 CA 热更新对所有 client（包括 SAGA）即时生效。替代方案（重建 client 实例）需要在所有持有 client 引用的位置同步替换，侵入性大且容易遗漏。

## Migration Plan

1. **阶段一（准备工作，不依赖平台）**：实现 TLS 配置段、`TransportProvider` 构造、CA 热更新 goroutine、AZ 进程内 TLS 监听、client 改造（接受 provider）。全部代码以 `tls.enabled = false` 默认值合入主干，不影响现有运行。
2. **阶段二（平台依赖就绪后，统一切换）**：`nsp_platform` 发布 `HTTPTransportProvider` 注入接口 -> 业务仓库升级 `go.mod` -> `initSaga()` 注入 provider -> AZ 侧开启 TLS 监听并上报 `https://` 地址 -> Top 侧开启 `tls.enabled`。此阶段是一个原子切换，所有 client 路径（包括 SAGA）同时获得 TLS 能力。
3. **阶段三（清理）**：确认所有 AZ 均已切换 HTTPS 后，可选地将 `tls.enabled` 默认值改为 `true`。

**回滚策略**：将 `NSP_TLS_ENABLED` 设为 `false` 即可恢复全链路 HTTP 明文通信。AZ 侧同步回退到 HTTP 监听并上报 `http://` 地址。无数据迁移需求。

## Open Questions

1. `nsp_platform` SAGA 模块提供 `*http.Client` 注入接口的具体形式和时间线需与平台团队确认。
2. Docker Compose 环境中证书文件的生成方式（手动生成 vs 集成 cfssl/step-ca 等轻量级 CA 工具链）需要确定。
3. 是否需要在 E2E 测试中覆盖 TLS 链路——如果需要，测试环境也需要证书生成和挂载流程。
