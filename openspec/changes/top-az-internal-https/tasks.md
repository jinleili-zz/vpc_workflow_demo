## 1. TLS 配置定义与加载

- [x] 1.1 在 `internal/config/config.go` 的 `NSPConfig` 中新增 `TLS TLSConfig` 字段，定义 `TLSConfig` 结构体（Enabled, Mode, CACertPath, CertPath, KeyPath, ClientAuth, CAReloadInterval, InsecureSkipVerify）
- [x] 1.2 在 `config/config.yaml` 中添加 `tls` 配置段及默认值（`client_auth` 默认 `true`）
- [x] 1.3 确认 `NSP_` 前缀环境变量覆盖机制对新增 TLS 字段生效（如 `NSP_TLS_ENABLED`、`NSP_TLS_CA_CERT_PATH`、`NSP_TLS_CLIENT_AUTH` 等）
- [x] 1.4 在启动流程中添加 TLS 配置校验逻辑：Top VPC 侧校验 CA 路径和客户端 cert/key 路径、AZ VPC 侧 process 模式校验 cert/key 路径、AZ VPC 侧 client_auth 启用时校验 CA 路径、lb 模式且 `tls.enabled=true` 时校验 `NSP_ADDR` 已设置（未设置则启动失败，不得静默回退 HTTP）

## 2. Top VPC 侧统一 mTLS Transport 构造

- [x] 2.1 定义 `reloadableTransport` 结构体（实现 `http.RoundTripper` 接口，内部使用 `atomic.Value` 持有 `*http.Transport`），在 `internal/bootstrap/bootstrap.go` 中新增 mTLS Transport 初始化函数：加载 CA 证书到 `RootCAs`、加载客户端证书到 `Certificates`，构造 `*tls.Config`、创建 `*http.Transport`，封装到 `reloadableTransport`，创建共享 `*http.Client{Transport: reloadableTransport}`
- [x] 2.2 实现证书文件变更检测后台 goroutine：按 `ca_reload_interval` 定期检查 CA 文件和客户端证书文件的修改时间，变更时重建 `*tls.Config` 和 `*http.Transport` 并原子更新到 `reloadableTransport` 内部
- [x] 2.3 修改 `internal/client/az_client.go` 的 `AZNSPClient` 构造函数，接收外部 `*http.Client`（TLS 启用时使用 mTLS client，未启用时使用默认 client）
- [x] 2.4 更新 `cmd/top_nsp/main.go` 的 bootstrap 流程，构造 mTLS `*http.Client` 并传入 `AZNSPClient` 和 SAGA Engine

## 3. AZ VPC 侧 mTLS 终止与地址上报

- [x] 3.1 修改 `internal/az/api/server.go` 的 HTTP Server 启动逻辑，当 `tls.enabled = true` 且 `tls.mode = "process"` 时使用 `ServeTLS()` 监听；当 `tls.client_auth = true` 时配置 `tls.Config.ClientAuth = tls.RequireAndVerifyClientCert` 并加载 `tls.ca_cert_path` 到 `ClientCAs`
- [x] 3.2 实现 `tls.Config.GetCertificate` 回调，支持每次 TLS 握手时从文件动态加载服务端证书（叶子证书热更新）
- [x] 3.3 实现 AZ 侧 ClientCAs 热更新：使用 `tls.Config.VerifyPeerCertificate` 自定义回调，在回调中从 `atomic.Value` 读取当前 CA 池进行客户端证书验证；启动后台 goroutine 按 `ca_reload_interval` 定期检查 CA 文件变更并原子更新 CA 池。此路径是 CA 三步轮换中 AZ 侧信任新 CA 的前置条件
- [x] 3.4 修改 AZ VPC NSP 自注册逻辑（`internal/az/api/server.go`）：`tls.mode = "process"` 且 TLS 启用时上报 `https://` scheme；`tls.mode = "lb"` 且 `tls.enabled = true` 时必须设置 `NSP_ADDR`（未设置则启动失败）；TLS 未启用时保持 `http://`
- [x] 3.5 更新 `cmd/az_nsp/main.go`，在 bootstrap 中传入 TLS 配置

## 4. SAGA Engine mTLS 适配（利用已有 HTTPClient 注入接口）

- [x] 4.1 在 `internal/bootstrap/bootstrap.go` 的 `initSaga()` 中，当 `tls.enabled = true` 时将共享 mTLS `*http.Client` 传入 `saga.Config.HTTPClient`
- [x] 4.2 验证 SAGA Engine 使用注入的 HTTPClient 发起所有 Top VPC -> AZ VPC 请求（通过单元测试或集成测试确认）

## 5. Docker 部署适配

- [x] 5.1 编写 openssl 证书生成脚本（`deployments/docker/certs/generate-certs.sh`），手动生成：CA 证书/私钥、Top 客户端证书/私钥（所有 Top VPC 服务共用）、各 AZ VPC 服务端证书/私钥
- [x] 5.2 修改 `deployments/docker/docker-compose.yml`，为 Top VPC NSP 容器挂载 CA 证书和客户端证书 volume，为 AZ VPC NSP 容器挂载 CA 证书和服务端证书 volume。VFW 服务容器不需要证书挂载
- [x] 5.3 在 Docker Compose 中为 VPC 服务添加 TLS 相关环境变量（`NSP_TLS_ENABLED`、`NSP_TLS_CA_CERT_PATH`、`NSP_TLS_CERT_PATH`、`NSP_TLS_KEY_PATH`、`NSP_TLS_CLIENT_AUTH`）。VFW 服务保持 `NSP_TLS_ENABLED=false` 或不设置

## 6. AK/SK 到 mTLS 迁移

- [x] 6.1 阶段二上线时：Top VPC 侧 mTLS client 保留 AK/SK 签名（`AZNSPClient` 继续签名、SAGA 步骤保留 `AuthAK`），AZ VPC 侧同时启用 mTLS 监听和 `AKSKAuthMiddleware`，确保 mTLS + AK/SK 共存
- [ ] 6.2 阶段三清理时：修改 `internal/az/api/server.go`，当 `tls.enabled = true` 且 `tls.client_auth = true` 时跳过 `AKSKAuthMiddleware`（mTLS 已提供身份认证），或通过 `auth.enable_auth = false` 显式关闭
- [ ] 6.3 阶段三清理时：Top VPC 侧移除 `AZNSPClient` 中的 AK/SK 签名逻辑，SAGA 步骤移除 `AuthAK` 字段

## 7. 测试

- [x] 7.1 为 `reloadableTransport` 和 mTLS Transport 构造逻辑编写单元测试（包括正常加载、文件不存在、证书无效、客户端证书缺失等场景）
- [x] 7.2 为 Top 侧 CA/证书热更新机制编写单元测试（模拟文件变更后验证 `reloadableTransport` 内部 Transport 更新）
- [x] 7.3 为 AZ VPC mTLS 监听编写单元测试（验证 ClientAuth 配置、GetCertificate 动态加载、ClientCAs 热更新、客户端证书验证通过/拒绝）
- [x] 7.4 为 AZ 侧 ClientCAs 热更新编写单元测试（模拟 CA 文件变更后验证 VerifyPeerCertificate 使用新 CA 池）
- [ ] 7.5 更新 E2E 测试脚本（`deployments/docker/test-e2e.sh`），增加 mTLS 模式下的 VPC/Subnet/PCCN 端到端验证（包含证书生成和挂载流程）
- [x] 7.6 验证 mTLS + AK/SK 共存阶段行为正确（阶段二回归测试）
- [x] 7.7 验证 VFW 路径在 VPC mTLS 启用后行为完全不变（AK/SK + HTTP 回归测试）
- [x] 7.8 验证 `tls.enabled = false` 时 VPC 服务行为完全不变（回归测试）
- [x] 7.9 验证 `tls.mode = "lb"` 且 `NSP_ADDR` 未设置时 AZ VPC 启动失败（fail-fast 测试）
