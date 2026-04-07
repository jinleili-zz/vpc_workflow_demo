## ADDED Requirements

### Requirement: AZ 进程内 TLS 监听
AZ NSP 进程（VPC 和 VFW）SHALL 在 TLS 启用时使用 `http.Server.ServeTLS()` 监听 HTTPS 端口，提供 TLS 终止能力。

#### Scenario: TLS 启用时 AZ 监听 HTTPS
- **WHEN** AZ NSP 启动且 `tls.enabled = true` 且 `tls.cert_path` 和 `tls.key_path` 指向有效的证书和私钥文件
- **THEN** AZ NSP MUST 使用 TLS 监听指定端口，接受 HTTPS 连接

#### Scenario: TLS 未启用时 AZ 保持 HTTP 监听
- **WHEN** AZ NSP 启动且 `tls.enabled = false`
- **THEN** AZ NSP MUST 使用普通 HTTP 监听指定端口（行为与当前一致）

#### Scenario: 证书文件无效时启动失败
- **WHEN** AZ NSP 启动且 `tls.enabled = true` 且证书或私钥文件不存在、格式错误或不匹配
- **THEN** 系统 MUST 输出明确的错误日志并终止启动

### Requirement: AZ 证书动态加载
AZ NSP SHALL 支持在不重启进程的情况下加载更新后的叶子证书，以支持证书续期。

#### Scenario: 证书文件更新后新连接使用新证书
- **WHEN** AZ NSP 运行中且 `tls.cert_path` 和 `tls.key_path` 指向的文件被替换为新证书/私钥
- **THEN** 后续新 TLS 握手 MUST 使用更新后的证书，已有连接不受影响

#### Scenario: 使用 GetCertificate 回调实现动态加载
- **WHEN** AZ NSP 配置 TLS 监听
- **THEN** 系统 MUST 使用 `tls.Config.GetCertificate` 回调从文件系统动态加载证书，而非在启动时一次性加载

### Requirement: AZ 注册地址 scheme 与 TLS 状态一致
AZ NSP 向 Top NSP 自注册时上报的地址 scheme SHALL 与自身的 TLS 监听状态一致。

#### Scenario: TLS 启用时上报 https 地址
- **WHEN** AZ NSP 启用 TLS 并向 Top 自注册
- **THEN** 上报地址 MUST 使用 `https://` scheme（如 `https://az-nsp-cn-north-1a:8080`）

#### Scenario: TLS 未启用时上报 http 地址
- **WHEN** AZ NSP 未启用 TLS 并向 Top 自注册
- **THEN** 上报地址 MUST 使用 `http://` scheme（行为与当前一致）

#### Scenario: 环境变量显式指定地址时使用指定值
- **WHEN** 环境变量 `NSP_ADDR` 或 `NSP_VFW_ADDR` 已设置
- **THEN** 系统 MUST 使用环境变量中的地址值，不自动推断 scheme

### Requirement: LB 终止模式支持
当 `tls.mode` 配置为 `lb` 时，AZ NSP SHALL 保持 HTTP 明文监听，但上报地址可通过环境变量指向 LB 的 HTTPS 入口。

#### Scenario: LB 模式下 AZ 保持 HTTP 监听
- **WHEN** AZ NSP 启动且 `tls.mode = "lb"`
- **THEN** AZ NSP MUST 使用普通 HTTP 监听，不加载证书文件

#### Scenario: LB 模式下通过环境变量指定 HTTPS 入口
- **WHEN** `tls.mode = "lb"` 且 `NSP_ADDR` 设置为 `https://lb-addr:443`
- **THEN** AZ 自注册 MUST 使用环境变量中的 `https://` 地址
