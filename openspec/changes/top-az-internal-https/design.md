## Context

当前系统存在两条独立的 Top -> AZ 调用路径，分别对应 VPC 服务和 VFW 服务：

**VPC 路径**（本次改造范围）：
1. **AZNSPClient** (`internal/client/az_client.go`) - Top VPC orchestrator 用于健康检查、状态轮询、Subnet/PCCN 直接调用
2. **SAGA Executor** (`nsp-platform/saga/executor.go`) - VPC/PCCN 创建/删除的同步步骤执行。SAGA 引擎已支持通过 `saga.Config.HTTPClient` 注入自定义 `*http.Client`，当前业务仓库未注入
3. Top API Server 中部分 Subnet/PCCN 操作调用 AZ VPC 端点

**VFW 路径**（不在本次范围）：
4. **SignedTracedClient** (`internal/client/signed_traced_client.go`) - Top VFW PolicyService 用于 VFW 策略下发
5. **散落的 `http.Get()`/`http.Post()`** - `orchestrator.CheckZonePolicies()` 等位置调用 AZ VFW 端点

当前 VPC 和 VFW 路径均使用 AK/SK 签名 + HTTP 明文。本次改造将 VPC 路径的安全模型从 AK/SK 切换为 mTLS（双向证书认证），VFW 路径保持 AK/SK 不变。两种安全模型独立运行，不叠加使用。

AZ VPC 自注册时硬编码 `http://` scheme（`internal/az/api/server.go:551`），Top registry 存储的 VPC AZ 地址也是 `http://` 前缀。

## Goals / Non-Goals

**Goals:**
- 为 Top VPC -> AZ VPC 内部出站链路引入 mTLS（双向证书认证），替代当前的 AK/SK 签名机制
- Top 验证 AZ VPC 服务端证书，AZ VPC 同时验证 Top 客户端证书，形成传输加密 + 双向身份认证的安全模型
- 统一 Top VPC 侧所有到 AZ VPC 的出站调用（`AZNSPClient`、SAGA、Top API Server 中的 Subnet/PCCN 操作）使用同一个 mTLS `*http.Client`
- 通过 SAGA 引擎已有的 `HTTPClient` 注入接口（`saga.Config.HTTPClient`）传入带 mTLS 的 client，无需平台侧额外改造
- 支持证书热更新：叶子证书续期和 CA 轮换场景下不中断服务
- AZ VPC 地址 scheme 从 `http://` 升级为 `https://`
- Docker Compose 环境使用 openssl 手动生成测试证书
- E2E 测试覆盖 mTLS 链路

**Non-Goals:**
- VFW 路径改造（VFW 继续使用 AK/SK + HTTP，不引入 TLS）
- mTLS 与 AK/SK 叠加使用（两种安全模型独立运行）
- Top 对外 API HTTPS（Top 对外入口不改）
- AZ -> Top 注册/心跳 HTTPS（不纳入实施范围）
- Redis / PostgreSQL 连接加密

## Decisions

### Decision 1: TLS 终止方式 - AZ VPC 进程内终止 vs 前置 LB/Ingress

**方案 A：AZ VPC 进程内终止 TLS（推荐）**

AZ VPC NSP 进程直接使用 Go 标准库 `tls.Listen()` / `http.Server.ServeTLS()` 监听 HTTPS 端口。AZ VFW NSP 不受影响，继续 HTTP 监听。

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

**决策：选择方案 A（AZ VPC 进程内终止 TLS）。** 理由：当前系统以 Docker Compose 部署为主，进程内终止 TLS 更简单直接。同时在配置中预留 `tls.mode` 字段（值为 `process` 或 `lb`），方便未来切换到 LB 终止模式——切换时 AZ VPC 进程只需关闭 TLS 监听并将上报地址指向 LB 的 HTTPS 入口即可。AZ VFW 进程不受本决策影响。

### Decision 2: Top VPC 侧统一 mTLS Transport 构造（可热更新 RoundTripper）

在 `internal/bootstrap/bootstrap.go` 中新增 TLS Transport 初始化逻辑（仅 Top VPC 服务使用）：

- 读取配置中的 CA 证书路径（用于验证 AZ 服务端证书）和客户端证书/私钥路径（用于 Top 向 AZ 证明自身身份），构造 `*tls.Config`，设置 `RootCAs` 和 `Certificates`
- Top 和 AZ 的证书由同一个内部 CA 签发，双方只需信任同一个 CA bundle
- 基于该 `*tls.Config` 创建 `*http.Transport`
- 引入 `reloadableTransport` 结构体，实现 `http.RoundTripper` 接口，内部通过 `atomic.Value` 持有当前 `*http.Transport` 指针
- 构造一个共享的 `*http.Client{Transport: reloadableTransport}`，注入到 Top VPC 侧所有到 AZ VPC 的出站 client：
  - `AZNSPClient` - 构造时接收该 `*http.Client`
  - Top API Server 中 Subnet/PCCN 操作调用 AZ VPC 端点的位置 - 统一使用该 client
  - SAGA Engine - 通过 `saga.Config.HTTPClient` 传入同一个 `*http.Client`（见 Decision 3）

Top VFW 服务不使用此 mTLS client，继续使用 `SignedTracedClient` + AK/SK。

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

### Decision 4: AZ VPC 地址 scheme 升级

AZ VPC 自注册时根据 TLS 配置和模式决定上报地址的 scheme：
- `tls.mode = "process"` 且 TLS 启用时：`https://az-nsp-{az}:{port}`
- `tls.mode = "lb"` 时：**必须**通过 `NSP_ADDR` 环境变量显式指定地址（因为 AZ 进程本身不监听 TLS，无法自动推断正确的 HTTPS 入口）；若环境变量未设置，回退到 `http://az-nsp-{az}:{port}`（HTTP 明文），并输出警告日志
- TLS 未启用时：保持 `http://az-nsp-{az}:{port}`

地址 scheme 由 AZ VPC 侧决定（AZ 知道自己是否启用了 TLS），Top 侧不做 scheme 推断或覆盖。Top VPC 侧的 mTLS client 需同时支持 http 和 https 目标（渐进式迁移期间可能混合存在）。

AZ VFW 不受影响，继续上报 `http://` 地址。

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

Top VPC 和 AZ VPC 复用相同的配置结构，但各字段的语义因角色不同而有所区别：
- **Top VPC 侧**：`ca_cert_path` 用于构造 `RootCAs`（验证 AZ VPC），`cert_path`/`key_path` 用于构造客户端证书（向 AZ VPC 证明身份）
- **AZ VPC 侧**：`ca_cert_path` 用于构造 `ClientCAs`（验证 Top 客户端证书），`cert_path`/`key_path` 用于 TLS 服务端监听

Top VFW 和 AZ VFW 不使用 TLS 配置（`tls.enabled` 保持 `false`），继续使用 AK/SK。

由于 Top 和 AZ VPC 的证书均由同一个内部 CA 签发，双方只需配置同一个 `ca_cert_path`。

环境变量覆盖（遵循 `NSP_` 前缀约定）：
- `NSP_TLS_ENABLED`
- `NSP_TLS_MODE`
- `NSP_TLS_CA_CERT_PATH`
- `NSP_TLS_CERT_PATH`
- `NSP_TLS_KEY_PATH`
- `NSP_TLS_CLIENT_AUTH`

## Risks / Trade-offs

**[风险] 证书文件挂载错误导致 VPC 服务启动失败** → 缓解：启动时校验证书文件可读性和有效性（到期时间检查），失败时输出明确错误日志。`tls.enabled = false` 时完全跳过 TLS 初始化，保持向后兼容。VFW 服务不受影响。

**[风险] 渐进式迁移期间 http/https 混合** → 缓解：Top VPC 侧 mTLS client 同时支持 http 和 https 目标。AZ VPC 地址 scheme 由 AZ 自行上报，Top 按实际 scheme 发起请求。

**[风险] CA 轮换时序错误导致 VPC 链路中断** → 缓解：文档化严格的三步轮换流程（双信任期 -> 切换证书 -> 移除旧 CA）。双方 CA reload 间隔默认 5 分钟，确保新 CA 文件部署后能被及时加载。VFW 链路不受 CA 轮换影响。

**[风险] mTLS 客户端证书过期导致所有 Top -> AZ VPC 调用失败** → 缓解：Top 侧通过 `reloadableTransport` 的后台 goroutine 同时监控客户端证书文件变更，续期后自动加载。启动时检查客户端证书有效期，临近过期输出警告日志。VFW 路径使用 AK/SK，不受证书过期影响。

**[Trade-off] 进程内 TLS vs LB 终止** → 选择进程内 TLS 增加了 AZ VPC 进程的证书管理负担，但避免了引入额外基础设施。通过 `tls.mode` 配置项保留未来切换到 LB 模式的能力。

**[Trade-off] `reloadableTransport` (RoundTripper wrapper) + `atomic.Value`** → 每次 HTTP 请求多一次 `atomic.Load` + 接口方法调用开销（纳秒级），换来 CA 和客户端证书热更新对所有 VPC client（包括 SAGA）即时透明生效。该方案利用 Go 标准库的 `http.RoundTripper` 接口，对 SAGA 引擎等调用方完全透明，无需任何调用方代码配合。

**[Trade-off] VPC/VFW 安全模型分离** → VPC 使用 mTLS，VFW 使用 AK/SK，两种安全模型独立运行不叠加。优点是每条路径的安全机制清晰简单；代价是两条路径需要不同的运维流程（VPC 运维证书，VFW 运维凭据）。

## Migration Plan

由于 SAGA 引擎已具备 `HTTPClient` 注入能力，不存在平台侧阻塞依赖，业务仓库可以独立完成全部改造。

1. **阶段一（代码改造）**：实现 mTLS 配置段、`reloadableTransport`（RoundTripper wrapper + atomic.Value）、CA/证书热更新 goroutine、AZ VPC 进程内 mTLS 监听（服务端证书 + ClientAuth）、Top VPC 侧 client 改造（`AZNSPClient` 和 SAGA 统一使用 mTLS `*http.Client`）。全部代码以 `tls.enabled = false` 默认值合入主干，不影响现有运行。VFW 服务不做任何改动。
2. **阶段二（证书部署与切换）**：使用 openssl 手动生成内部 CA 及双方证书 -> Docker Compose 挂载证书 volume -> AZ VPC 侧开启 mTLS 监听并上报 `https://` 地址 -> Top VPC 侧开启 `tls.enabled` -> 验证全链路 mTLS。
3. **阶段三（清理）**：确认所有 AZ VPC 均已切换 HTTPS 后，可选地移除 VPC 路径上残留的 AK/SK 签名逻辑。

**回滚策略**：将 VPC 服务的 `NSP_TLS_ENABLED` 设为 `false` 即可恢复 VPC 链路 HTTP 明文通信。AZ VPC 侧同步回退到 HTTP 监听并上报 `http://` 地址。VFW 链路不受影响。无数据迁移需求。

## Resolved Questions

1. **证书生成方式**：使用 openssl 手动生成测试证书（CA + Top 客户端证书 + 各 AZ VPC 服务端证书），不引入 cfssl/step-ca 等额外工具链。
2. **E2E 测试 mTLS 覆盖**：E2E 测试需要覆盖 mTLS 链路，测试环境包含证书生成和挂载流程。
3. **客户端证书粒度**：Top VPC 服务共用一个客户端证书，不按服务角色区分。（VFW 不使用 mTLS，无需客户端证书。）
