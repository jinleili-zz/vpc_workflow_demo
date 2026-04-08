## Context

当前 Top NSP（VPC + VFW）与 AZ NSP（VPC + VFW）之间的所有内部 HTTP 调用均为明文传输。调用链涉及四条独立的 HTTP client 路径：

1. **AZNSPClient** (`internal/client/az_client.go`) - Top VPC orchestrator 用于健康检查、状态轮询、Subnet/PCCN 直接调用
2. **SignedTracedClient** (`internal/client/signed_traced_client.go`) - Top VFW PolicyService 和 Top API Server 用于 VFW 策略下发、Subnet 状态/删除
3. **SAGA Executor** (`nsp-platform/saga/executor.go`) - SAGA 引擎支持通过 `saga.Config.HTTPClient` / `saga.ExecutorConfig.HTTPClient` 注入自定义 `*http.Client`，当前业务仓库未注入，使用引擎内部自建的 plain `*http.Client`
4. **散落的 `http.Get()`/`http.Post()`** - `orchestrator.CheckZonePolicies()` 等位置使用 stdlib 默认 client

AK/SK 签名机制已覆盖路径 1、2、3（SAGA 通过 `credStore` 按步骤签名），但路径 4 完全绕过签名和 tracing。

AZ 自注册时硬编码 `http://` scheme（`internal/az/api/server.go:551`、`internal/az/vfw/api/server.go:252`），Top registry 存储的地址也是 `http://` 前缀。

## Goals / Non-Goals

**Goals:**
- 为 Top -> AZ 内部出站链路提供传输层加密（TLS），与现有 AK/SK 叠加形成双层安全
- 实现 mTLS 双向证书认证：Top 验证 AZ 服务端证书，AZ 同时验证 Top 客户端证书，形成"传输加密 + 双向身份认证 + 请求签名"的三层安全模型
- 统一 Top 侧所有 Top -> AZ 出站调用的 TLS Transport，消除 client 路径分裂
- 通过 SAGA 引擎已有的 `HTTPClient` 注入接口（`saga.Config.HTTPClient`）传入带 mTLS 的 client，无需平台侧额外改造
- 支持证书热更新：叶子证书续期和 CA 轮换场景下不中断服务
- AZ 地址 scheme 从 `http://` 升级为 `https://`
- 明确 `vpc_workflow_demo`（业务仓库）与 `nsp_platform`（平台仓库）的职责边界

**Non-Goals:**
- Top 对外 API HTTPS（Top 对外入口不改）
- AZ -> Top 注册/心跳 HTTPS（不纳入实施范围）
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

### Decision 2: Top 侧统一 mTLS Transport 构造（可热更新 RoundTripper）

在 `internal/bootstrap/bootstrap.go` 中新增 TLS Transport 初始化逻辑：

- 读取配置中的 CA 证书路径（用于验证 AZ 服务端证书）和客户端证书/私钥路径（用于 Top 向 AZ 证明自身身份），构造 `*tls.Config`，设置 `RootCAs` 和 `Certificates`
- Top 和 AZ 的证书由同一个内部 CA 签发，双方只需信任同一个 CA bundle
- 基于该 `*tls.Config` 创建 `*http.Transport`
- 引入 `reloadableTransport` 结构体，实现 `http.RoundTripper` 接口，内部通过 `atomic.Value` 持有当前 `*http.Transport` 指针
- 构造一个共享的 `*http.Client{Transport: reloadableTransport}`，注入到所有 Top -> AZ 出站 client：
  - `AZNSPClient` - 构造时接收该 `*http.Client`
  - `SignedTracedClient` / `TracedClient` - 同上
  - 散落的 `http.Get()`/`http.Post()` - 替换为使用统一 client 的调用
  - SAGA Engine - 通过 `saga.Config.HTTPClient` 传入同一个 `*http.Client`（见 Decision 3）

**关键设计点**：`reloadableTransport` 实现 `http.RoundTripper` 接口，`RoundTrip()` 方法每次调用时通过 `atomic.Load` 获取当前 `*http.Transport` 后委托执行。CA 或客户端证书文件变更时，bootstrap 的后台 goroutine 重建内部 `*http.Transport` 并原子替换，所有使用该 `*http.Client` 的调用方（包括 SAGA Executor 的 `e.client.Do(req)`）自动获得新 Transport，无需重建任何 client 或 Engine 实例。

这种方式比 `TransportProvider` 函数回调更符合 Go 惯例：直接利用 `http.RoundTripper` 接口，对调用方完全透明，尤其适配 SAGA 引擎在构造时持有 `*http.Client` 并在整个生命周期内复用的模式。

**不新建独立 TLS 工具包**。`reloadableTransport` 和 TLS 构造逻辑直接在 bootstrap 中实现，作为初始化步骤之一。如果未来需要在更多场景复用，再提取为独立 package。

### Decision 3: SAGA 引擎 mTLS 适配 - 利用已有 HTTPClient 注入接口

**SAGA 平台侧已具备 HTTPClient 注入能力，不需要额外的平台侧改造。**

`nsp-platform/saga` 的 `Config` 和 `ExecutorConfig` 均已提供 `HTTPClient *http.Client` 字段（`engine.go:33-35`、`executor.go:37-39`）。当 `HTTPClient` 非 nil 时，SAGA Executor 使用注入的 client 而非内部自建的 plain client（`executor.go:63-68`）。Executor 在构造时持有该 `*http.Client` 引用并在整个生命周期复用。

**业务仓库（`vpc_workflow_demo`）职责：**
- 构造 `reloadableTransport`（与 Decision 2 相同的实例），创建 `*http.Client{Transport: reloadableTransport}`
- 在 `internal/bootstrap/bootstrap.go` 的 `initSaga()` 中将该 `*http.Client` 传入 `saga.Config.HTTPClient`
- 由于 SAGA Executor 通过 `e.client.Do(req)` 发起请求，而 `*http.Client.Transport` 指向 `reloadableTransport`，每次 `RoundTrip()` 调用都会从 `atomic.Value` 获取最新的内部 `*http.Transport`，CA/客户端证书热更新对 SAGA 完全透明

**本仓库不需要修改 `nsp_platform` 代码，也不存在平台侧阻塞依赖。** 业务仓库可以独立完成全部 mTLS 改造并一次性上线。

### Decision 4: AZ 地址 scheme 升级

AZ 自注册时根据 TLS 配置和模式决定上报地址的 scheme：
- `tls.mode = "process"` 且 TLS 启用时：`https://az-nsp-{az}:{port}`
- `tls.mode = "lb"` 时：**必须**通过 `NSP_ADDR` / `NSP_VFW_ADDR` 环境变量显式指定地址（因为 AZ 进程本身不监听 TLS，无法自动推断正确的 HTTPS 入口）；若环境变量未设置，回退到 `http://az-nsp-{az}:{port}`（HTTP 明文），并输出警告日志
- TLS 未启用时：保持 `http://az-nsp-{az}:{port}`

地址 scheme 由 AZ 侧决定（AZ 知道自己是否启用了 TLS），Top 侧不做 scheme 推断或覆盖。Top 侧的 TLS client 需同时支持 http 和 https 目标（渐进式迁移期间可能混合存在）。

### Decision 5: 证书热更新策略

**AZ 侧叶子证书续期（CA 不变）：**
1. 使用 `tls.Config.GetCertificate` 回调（AZ 侧服务端证书），每次 TLS 握手时从文件动态加载证书
2. 证书文件被替换后，新连接自动使用新证书，已建立的连接不受影响
3. 无需 SIGHUP 或重启

**AZ 侧 mTLS 客户端 CA 热更新：**
1. AZ 侧的 `tls.Config.ClientCAs`（用于验证 Top 客户端证书的 CA 池）使用 `tls.Config.VerifyPeerCertificate` 自定义回调，在回调中从 `atomic.Value` 读取当前 CA 池进行验证
2. 后台 goroutine 定期检查 CA 文件变更并原子更新 CA 池

**Top 侧 CA 和客户端证书热更新：**
1. Top 侧的 `RootCAs`（验证 AZ 服务端证书）和 `Certificates`（Top 客户端证书）均需支持动态更新
2. 实现方式：`reloadableTransport` 的后台 goroutine 定期（如 5 分钟）检查 CA 文件和客户端证书文件的修改时间，若变化则重建 `*tls.Config` 和 `*http.Transport`，原子替换到 `reloadableTransport` 内部
3. CA 轮换顺序：
   - 第一步：在双方 CA bundle 中同时包含旧 CA 和新 CA（双信任期）
   - 第二步：AZ 侧切换到新 CA 签发的服务端证书，Top 侧切换到新 CA 签发的客户端证书
   - 第三步：从双方 CA bundle 中移除旧 CA
4. 双信任期确保切换过程中不中断

**Transport 热更新机制：**
- `reloadableTransport` 内部使用 `atomic.Value` 存储当前 `*http.Transport` 指针
- `RoundTrip()` 方法每次调用时通过 `atomic.Load` 获取最新 Transport 并委托执行
- CA 或客户端证书更新后，后台 goroutine 重建 Transport 并原子替换
- 所有使用该 `*http.Client` 的调用方（包括 SAGA Executor）自动获得新 Transport
- 旧 Transport 上的活跃连接自然结束后由 GC 回收

### Decision 6: 配置项设计

在 `NSPConfig` 中新增 `TLS` 配置段：

```yaml
tls:
  enabled: false                    # 总开关
  mode: "process"                   # "process" (进程内终止) 或 "lb" (LB 终止)
  ca_cert_path: ""                  # CA 证书路径（双方共用同一个 CA）
                                    # Top 侧：用于验证 AZ 服务端证书
                                    # AZ 侧：用于验证 Top 客户端证书
  cert_path: ""                     # 证书路径
                                    # AZ 侧：服务端证书（TLS 监听用）
                                    # Top 侧：客户端证书（mTLS 向 AZ 证明身份用）
  key_path: ""                      # 私钥路径（与 cert_path 对应）
  client_auth: true                 # AZ 侧：是否要求验证客户端证书（mTLS）
  ca_reload_interval: "5m"          # CA 文件和证书文件变更检查间隔
  insecure_skip_verify: false       # 仅用于测试环境
```

Top 和 AZ 复用相同的配置结构，但各字段的语义因角色不同而有所区别：
- **Top 侧**：`ca_cert_path` 用于构造 `RootCAs`（验证 AZ），`cert_path`/`key_path` 用于构造客户端证书（向 AZ 证明身份）
- **AZ 侧**：`ca_cert_path` 用于构造 `ClientCAs`（验证 Top 客户端证书），`cert_path`/`key_path` 用于 TLS 服务端监听

由于 Top 和 AZ 的证书均由同一个内部 CA 签发，双方只需配置同一个 `ca_cert_path`。

环境变量覆盖（遵循 `NSP_` 前缀约定）：
- `NSP_TLS_ENABLED`
- `NSP_TLS_MODE`
- `NSP_TLS_CA_CERT_PATH`
- `NSP_TLS_CERT_PATH`
- `NSP_TLS_KEY_PATH`
- `NSP_TLS_CLIENT_AUTH`

## Risks / Trade-offs

**[风险] 证书文件挂载错误导致启动失败** → 缓解：启动时校验证书文件可读性和有效性（到期时间检查），失败时输出明确错误日志。`tls.enabled = false` 时完全跳过 TLS 初始化，保持向后兼容。

**[风险] 渐进式迁移期间 http/https 混合** → 缓解：Top 侧 TLS Transport 配置为可选（遇到 `http://` 目标时不强制 TLS）。AZ 地址 scheme 由 AZ 自行上报，Top 按实际 scheme 发起请求。

**[风险] CA 轮换时序错误导致连接中断** → 缓解：文档化严格的三步轮换流程（双信任期 -> 切换证书 -> 移除旧 CA）。双方 CA reload 间隔默认 5 分钟，确保新 CA 文件部署后能被及时加载。

**[风险] mTLS 客户端证书过期导致所有 Top -> AZ 调用失败** → 缓解：Top 侧通过 `reloadableTransport` 的后台 goroutine 同时监控客户端证书文件变更，续期后自动加载。启动时检查客户端证书有效期，临近过期输出警告日志。

**[Trade-off] 进程内 TLS vs LB 终止** → 选择进程内 TLS 增加了每个 AZ 进程的证书管理负担，但避免了引入额外基础设施。通过 `tls.mode` 配置项保留未来切换到 LB 模式的能力。

**[Trade-off] `reloadableTransport` (RoundTripper wrapper) + `atomic.Value`** → 每次 HTTP 请求多一次 `atomic.Load` + 接口方法调用开销（纳秒级），换来 CA 和客户端证书热更新对所有 client（包括 SAGA）即时透明生效。该方案利用 Go 标准库的 `http.RoundTripper` 接口，对 SAGA 引擎等调用方完全透明，无需任何调用方代码配合。

**[Trade-off] 同一 CA vs 分离 CA** → 选择同一 CA 签发 Top 客户端证书和 AZ 服务端证书，简化了部署（双方只需一个 CA bundle），代价是无法通过 CA 粒度区分客户端和服务端角色。如果未来需要更细粒度的身份管理，可切换到分离 CA 模型。

## Migration Plan

由于 SAGA 引擎已具备 `HTTPClient` 注入能力，不存在平台侧阻塞依赖，业务仓库可以独立完成全部改造。

1. **阶段一（代码改造）**：实现 mTLS 配置段、`reloadableTransport`（RoundTripper wrapper + atomic.Value）、CA/证书热更新 goroutine、AZ 进程内 mTLS 监听（服务端证书 + ClientAuth）、Top 侧 client 改造（统一使用带 mTLS 的 `*http.Client`）、SAGA 注入（`saga.Config.HTTPClient`）。全部代码以 `tls.enabled = false` 默认值合入主干，不影响现有运行。
2. **阶段二（证书部署与切换）**：生成内部 CA 及双方证书 -> Docker Compose 挂载证书 volume -> AZ 侧开启 mTLS 监听并上报 `https://` 地址 -> Top 侧开启 `tls.enabled` -> 验证全链路 mTLS。此阶段是一个原子切换，所有 client 路径（包括 SAGA）同时获得 mTLS 能力。
3. **阶段三（清理）**：确认所有 AZ 均已切换 HTTPS 后，可选地将 `tls.enabled` 默认值改为 `true`。

**回滚策略**：将 `NSP_TLS_ENABLED` 设为 `false` 即可恢复全链路 HTTP 明文通信。AZ 侧同步回退到 HTTP 监听并上报 `http://` 地址。无数据迁移需求。

## Open Questions

1. Docker Compose 环境中证书文件的生成方式（手动生成 vs 集成 cfssl/step-ca 等轻量级 CA 工具链）需要确定。
2. 是否需要在 E2E 测试中覆盖 mTLS 链路——如果需要，测试环境也需要证书生成和挂载流程。
3. mTLS 的客户端证书是否需要按 Top 服务角色区分（如 Top VPC 和 Top VFW 各自独立证书），还是共用一个客户端证书。
