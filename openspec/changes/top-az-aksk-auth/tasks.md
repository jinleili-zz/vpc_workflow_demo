## 1. AK/SK配置加载（aksk-config）

- [ ] 1.1 在`internal/config/config.go`中添加AuthConfig结构体，包含Credentials字段
- [ ] 1.2 修改配置文件结构（configs/config.yaml），添加auth.credentials配置段
- [ ] 1.3 实现环境变量解析功能，支持`${ENV_VAR}`格式替换
- [ ] 1.4 在bootstrap.Initialize()中加载credentials并创建CredentialStore
- [ ] 1.5 为Top-NSP创建Signer实例
- [ ] 1.6 为AZ-NSP创建Verifier实例

## 2. AZ-NSP认证中间件（az-auth-middleware）

- [ ] 2.1 在`cmd/az_nsp/main.go`中引入bootstrap初始化
- [ ] 2.2 将Verifier传递给AZ API Server
- [ ] 2.3 在`internal/az/api/server.go`中添加AK/SK认证中间件
- [ ] 2.4 配置免认证路径：`/api/v1/health`
- [ ] 2.5 添加配置项控制认证开关（EnableAuth）

## 3. AZNSPClient签名改造（top-request-signer）

- [ ] 3.1 在`AZNSPClient`结构体中添加`signer *auth.Signer`字段
- [ ] 3.2 修改`NewAZNSPClient()`构造函数，接收signer参数
- [ ] 3.3 修改`NewAZNSPClientWithTrace()`构造函数，接收signer参数
- [ ] 3.4 为`HealthCheck()`方法添加签名逻辑
- [ ] 3.5 为`CreateVPC()`方法添加签名逻辑
- [ ] 3.6 为`DeleteVPC()`方法添加签名逻辑
- [ ] 3.7 为`GetVPCStatus()`方法添加签名逻辑
- [ ] 3.8 为`CreateSubnet()`方法添加签名逻辑
- [ ] 3.9 为`GetPCCNStatus()`方法添加签名逻辑
- [ ] 3.10 为`DeletePCCN()`方法添加签名逻辑

## 4. Saga签名支持（saga-auth）

- [ ] 4.1 在bootstrap.initSaga()中传入CredentialStore到Saga Config
- [ ] 4.2 修改`CreateRegionVPC()`中Saga Step构建，设置AuthAK字段
- [ ] 4.3 修改`CreatePCCN()`中Saga Step构建，设置AuthAK字段
- [ ] 4.4 修改`DeletePCCN()`中Saga Step构建，设置AuthAK字段
- [ ] 4.5 修改`NewOrchestrator()`构造函数，接收Signer参数并创建AZNSPClient

## 5. 直接HTTP调用签名改造

- [ ] 5.1 创建`SignedTracedClient`包装器，组合TracedClient和Signer
- [ ] 5.2 修改`internal/top/api/server.go`，使用SignedTracedClient
- [ ] 5.3 为GetSubnetStatus调用添加签名
- [ ] 5.4 为DeleteSubnet调用添加签名
- [ ] 5.5 为DeleteSubnetByID调用添加签名

## 6. VFW服务签名改造

- [ ] 6.1 在`PolicyService`中添加Signer字段
- [ ] 6.2 修改`NewPolicyService()`构造函数，接收Signer参数
- [ ] 6.3 为`CreatePolicy()`中的HTTP调用添加签名
- [ ] 6.4 修改VFW Server初始化，传入Signer

## 7. 启动入口改造

- [ ] 7.1 修改`cmd/top_nsp/main.go`，加载auth配置并创建Signer
- [ ] 7.2 修改`cmd/top_nsp/main.go`，将Signer传递给Orchestrator和VFW Service
- [ ] 7.3 修改`cmd/az_nsp/main.go`，加载auth配置并创建Verifier
- [ ] 7.4 修改`cmd/az_nsp/main.go`，将Verifier传递给API Server

## 8. 测试与验证

- [ ] 8.1 编写AK/SK配置加载的单元测试
- [ ] 8.2 编写AZ认证中间件的单元测试
- [ ] 8.3 编写AZNSPClient签名的单元测试
- [ ] 8.4 运行集成测试验证完整调用链
- [ ] 8.5 验证免认证路径（health check）正常工作
- [ ] 8.6 验证AZ→Top的注册/心跳不受影响
