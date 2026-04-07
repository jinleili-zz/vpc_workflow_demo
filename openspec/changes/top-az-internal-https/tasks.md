## 1. TLS 配置定义与加载

- [ ] 1.1 在 `internal/config/config.go` 的 `NSPConfig` 中新增 `TLS TLSConfig` 字段，定义 `TLSConfig` 结构体（Enabled, Mode, CACertPath, CertPath, KeyPath, CAReloadInterval, InsecureSkipVerify）
- [ ] 1.2 在 `config/config.yaml` 中添加 `tls` 配置段及默认值
- [ ] 1.3 确认 `NSP_` 前缀环境变量覆盖机制对新增 TLS 字段生效（如 `NSP_TLS_ENABLED`、`NSP_TLS_CA_CERT_PATH` 等）
- [ ] 1.4 在启动流程中添加 TLS 配置校验逻辑：Top 侧校验 CA 路径、AZ 侧 process 模式校验 cert/key 路径、lb 模式跳过 cert/key 校验

## 2. Top 侧统一 TLS Transport 构造

- [ ] 2.1 定义 `TransportProvider` 函数类型（`type TransportProvider func() *http.Transport`），在 `internal/bootstrap/bootstrap.go` 中新增 TLS Transport 初始化函数：加载 CA 证书、构造 `*tls.Config`、创建 `*http.Transport`，封装为 `TransportProvider`（内部使用 `atomic.Value`）
- [ ] 2.2 实现 CA 文件变更检测后台 goroutine：按 `ca_reload_interval` 定期检查文件修改时间，变更时重建 Transport 并原子更新到 provider 内部
- [ ] 2.3 修改 `internal/client/az_client.go` 的 `AZNSPClient` 构造函数，接收 `TransportProvider`；修改 `do()` 方法在每次请求时调用 provider 获取当前 Transport
- [ ] 2.4 修改 `internal/client/signed_traced_client.go` 的 `SignedTracedClient` 构造函数，接收 `TransportProvider`；确保每次 `Do()` 调用时通过 provider 获取当前 Transport
- [ ] 2.5 将 `internal/top/orchestrator/orchestrator.go` 中 `CheckZonePolicies` 的 `http.Get()` 调用替换为使用统一 client
- [ ] 2.6 检查并替换 Top 侧其他散落的 `http.Get()`/`http.Post()` 直接调用（如 Top API Server 中的调用），统一收口到带 TLS 的 client 路径
- [ ] 2.7 更新 `cmd/top_nsp/main.go` 和 `cmd/top_nsp_vfw/main.go` 的 bootstrap 流程，传入 TLS Transport

## 3. AZ 侧 TLS 终止与地址上报

- [ ] 3.1 修改 `internal/az/api/server.go` 的 HTTP Server 启动逻辑，当 `tls.enabled = true` 且 `tls.mode = "process"` 时使用 `ServeTLS()` 监听
- [ ] 3.2 实现 `tls.Config.GetCertificate` 回调，支持每次 TLS 握手时从文件动态加载证书（叶子证书热更新）
- [ ] 3.3 修改 AZ VPC NSP 自注册逻辑（`internal/az/api/server.go`）：`tls.mode = "process"` 且 TLS 启用时上报 `https://` scheme；`tls.mode = "lb"` 时依赖 `NSP_ADDR` 环境变量（未设置则回退 `http://` 并输出警告）；TLS 未启用时保持 `http://`
- [ ] 3.4 对 `internal/az/vfw/api/server.go` 执行与 3.1-3.3 相同的改造（VFW AZ 侧 TLS 监听和地址上报，LB 模式同理依赖 `NSP_VFW_ADDR`）
- [ ] 3.5 更新 `cmd/az_nsp/main.go` 和 `cmd/az_nsp_vfw/main.go`，在 bootstrap 中传入 TLS 配置

## 4. SAGA 引擎 TLS 适配（阻塞依赖，需先完成平台侧变更）

- [ ] 4.1 向平台团队提出 `nsp_platform` SAGA 模块变更需求：在 `saga.ExecutorConfig` 中新增 `HTTPTransportProvider func() *http.Transport` 字段，`NewExecutor()` 中若非 nil 则每次请求通过 provider 获取 Transport
- [ ] 4.2 平台侧发布新版本后，业务仓库升级 `go.mod` 中 `nsp-platform` 依赖版本
- [ ] 4.3 在 `internal/bootstrap/bootstrap.go` 的 `initSaga()` 中将 `TransportProvider` 传入 `saga.ExecutorConfig.HTTPTransportProvider`

## 5. Docker 部署适配

- [ ] 5.1 编写测试用 CA 和叶子证书的生成脚本（使用 openssl 或 cfssl），放置在 `deployments/docker/certs/` 目录
- [ ] 5.2 修改 `deployments/docker/docker-compose.yml`，为 Top NSP 容器挂载 CA 证书 volume，为 AZ NSP 容器挂载证书和私钥 volume
- [ ] 5.3 在 Docker Compose 中为各服务添加 TLS 相关环境变量（`NSP_TLS_ENABLED`、`NSP_TLS_CA_CERT_PATH`、`NSP_TLS_CERT_PATH`、`NSP_TLS_KEY_PATH`）
- [ ] 5.4 更新 `deployments/docker/build-images.sh`（如需要），确保证书目录包含在镜像或 volume 中

## 6. 测试

- [ ] 6.1 为 TLS Transport 构造和 CA 加载逻辑编写单元测试（包括正常加载、文件不存在、证书无效等场景）
- [ ] 6.2 为 CA 热更新机制编写单元测试（模拟文件变更后验证 Transport 更新）
- [ ] 6.3 为 AZ TLS 监听和 GetCertificate 动态加载编写单元测试
- [ ] 6.4 更新 E2E 测试脚本（`deployments/docker/test-e2e.sh`），增加 TLS 模式下的端到端验证
- [ ] 6.5 验证 `tls.enabled = false` 时系统行为完全不变（回归测试）
