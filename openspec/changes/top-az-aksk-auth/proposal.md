## Why

在实际的生产部署中，Top-NSP和AZ-NSP位于不同的地域和集群，跨地域的HTTP通信需要进行身份认证。当前系统没有任何认证机制，存在安全隐患。需要实现基于AK/SK的HMAC签名认证，确保只有持有合法凭证的Top-NSP才能调用AZ-NSP的API。

## What Changes

- **AK/SK配置加载**: Top-NSP和AZ-NSP启动时从配置文件加载AK/SK凭证
- **AZ-NSP请求验证**: AZ-NSP对所有业务API请求进行AK/SK签名验证
- **Top-NSP请求签名**: Top-NSP对发往AZ-NSP的所有HTTP请求进行签名
- **免认证路径**: 健康检查接口保持免认证，AZ→Top的注册/心跳走内网免认证
- **Saga签名支持**: Saga引擎执行的HTTP请求支持AK/SK签名

## Capabilities

### New Capabilities

- `aksk-config`: AK/SK凭证的配置加载能力，支持从配置文件读取明文凭证
- `az-auth-middleware`: AZ-NSP的AK/SK认证中间件，验证入站请求签名
- `top-request-signer`: Top-NSP的HTTP客户端签名能力，为出站请求添加签名
- `saga-auth`: Saga引擎的请求签名支持，为Saga步骤执行添加AK/SK认证

### Modified Capabilities

无（新增能力，不修改现有规格）

## Impact

### 受影响的代码模块

**VPC服务链路:**

| 模块 | 文件 | 改动类型 |
|------|------|----------|
| 配置加载 | `internal/config/config.go` | 新增AK/SK配置字段 |
| 配置文件 | `config/config.yaml` | 添加auth.credentials配置段 |
| 启动入口 | `cmd/top_nsp/main.go` | 加载凭证、初始化Signer |
| 启动入口 | `cmd/az_nsp/main.go` | 加载凭证、初始化Verifier、添加中间件 |
| HTTP客户端 | `internal/client/az_client.go` | 添加Signer、为所有方法签名请求 |
| Saga编排 | `internal/top/orchestrator/orchestrator.go` | 构建Saga Step时设置AuthAK |
| Bootstrap | `internal/bootstrap/bootstrap.go` | Saga初始化时传入CredentialStore |
| AZ VPC API | `internal/az/api/server.go` | 添加AK/SK认证中间件 |
| Top API | `internal/top/api/server.go` | 直接HTTP调用添加签名 |

**VFW服务链路:**

| 模块 | 文件 | 改动类型 |
|------|------|----------|
| Top VFW入口 | `cmd/top_nsp_vfw/main.go` | 加载凭证、初始化Signer |
| AZ VFW入口 | `cmd/az_nsp_vfw/main.go` | 加载凭证、初始化Verifier、添加中间件 |
| Top VFW API | `internal/top/vfw/api/server.go` | 添加认证中间件 |
| Top VFW Service | `internal/top/vfw/service/policy.go` | HTTP调用添加签名 |
| AZ VFW API | `internal/az/vfw/api/server.go` | 添加AK/SK认证中间件 |

### HTTP交互点清单（需要签名）

**Top-NSP-VPC → AZ-NSP-VPC 业务调用（需要签名）:**

| 调用方 | 目标接口 | 当前实现 | 改造方式 |
|--------|----------|----------|----------|
| AZNSPClient | GET /api/v1/health | az_client.go | 添加Signer签名 |
| AZNSPClient | POST /api/v1/vpc | Saga执行 | Step.AuthAK |
| AZNSPClient | DELETE /api/v1/vpc/{name} | Saga执行 | Step.AuthAK |
| AZNSPClient | GET /api/v1/vpc/{name}/status | az_client.go | 添加Signer签名 |
| AZNSPClient | POST /api/v1/subnet | az_client.go | 添加Signer签名 |
| AZNSPClient | GET /api/v1/subnet/{name}/status | top/api/server.go直接调用 | 添加签名逻辑 |
| AZNSPClient | DELETE /api/v1/subnet/{name} | top/api/server.go直接调用 | 添加签名逻辑 |
| AZNSPClient | POST /api/v1/pccn | Saga执行 | Step.AuthAK |
| AZNSPClient | DELETE /api/v1/pccn/{name} | Saga执行 | Step.AuthAK |
| AZNSPClient | GET /api/v1/pccn/{name}/status | az_client.go | 添加Signer签名 |

**Top-NSP-VFW → AZ-NSP-VFW 业务调用（需要签名）:**

| 调用方 | 目标接口 | 当前实现 | 改造方式 |
|--------|----------|----------|----------|
| PolicyService | POST /api/v1/firewall/policy | vfw/service/policy.go http.Post | 添加Signer签名 |

**AZ-NSP → Top-NSP（不需要签名）:**
- POST /api/v1/register/az（AZ VPC注册，走内网）
- POST /api/v1/heartbeat（AZ VPC心跳，走内网）
- POST /api/v1/register/az（AZ VFW注册，走内网）
- POST /api/v1/heartbeat（AZ VFW心跳，走内网）

**AZ-NSP ↔ Worker（不受影响）:**
- Redis/asynq消息队列通信，不需要修改

### 依赖

- `github.com/jinleili-zz/nsp-platform/auth`: 已支持AK/SK签名和验证
- `github.com/jinleili-zz/nsp-platform/saga`: 已支持Step.AuthAK和CredentialStore

### 不受影响的系统

- Worker与AZ-NSP之间的Redis队列通信
- 外部调用者对Top-NSP的API调用（本次不涉及）
