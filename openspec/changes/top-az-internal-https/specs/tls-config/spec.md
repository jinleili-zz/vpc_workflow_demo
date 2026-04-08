## ADDED Requirements

### Requirement: TLS 配置段定义
系统 SHALL 在 `NSPConfig` 中新增 `TLS` 配置段，包含 TLS 启用开关、模式选择、证书路径和 CA 证书路径等字段。仅 VPC 服务（Top VPC NSP 和 AZ VPC NSP）使用此配置；VFW 服务保持 `tls.enabled = false`。

#### Scenario: 配置文件包含完整 TLS 配置段
- **WHEN** `config/config.yaml` 加载配置
- **THEN** 系统 MUST 支持以下配置字段：`tls.enabled`（bool）、`tls.mode`（string，值为 `process` 或 `lb`）、`tls.ca_cert_path`（string）、`tls.cert_path`（string）、`tls.key_path`（string）、`tls.client_auth`（bool）、`tls.ca_reload_interval`（duration string）、`tls.insecure_skip_verify`（bool）

#### Scenario: TLS 配置缺省值
- **WHEN** 配置文件中未指定 TLS 配置段
- **THEN** `tls.enabled` MUST 默认为 `false`，`tls.mode` MUST 默认为 `"process"`，`tls.client_auth` MUST 默认为 `true`，`tls.ca_reload_interval` MUST 默认为 `"5m"`，`tls.insecure_skip_verify` MUST 默认为 `false`

### Requirement: TLS 环境变量覆盖
系统 SHALL 支持通过 `NSP_` 前缀的环境变量覆盖 TLS 配置项，与现有配置覆盖机制一致。

#### Scenario: 环境变量覆盖 TLS 启用开关
- **WHEN** 环境变量 `NSP_TLS_ENABLED` 设置为 `true`
- **THEN** 系统 MUST 将 `tls.enabled` 覆盖为 `true`，无论配置文件中的值

#### Scenario: 环境变量覆盖 CA 证书路径
- **WHEN** 环境变量 `NSP_TLS_CA_CERT_PATH` 设置为 `/certs/ca.pem`
- **THEN** 系统 MUST 将 `tls.ca_cert_path` 覆盖为 `/certs/ca.pem`

#### Scenario: 环境变量覆盖证书和私钥路径
- **WHEN** 环境变量 `NSP_TLS_CERT_PATH` 和 `NSP_TLS_KEY_PATH` 分别设置
- **THEN** 系统 MUST 将对应的 `tls.cert_path` 和 `tls.key_path` 字段覆盖

### Requirement: TLS 配置校验
系统 SHALL 在启动时校验 TLS 配置的完整性和一致性。

#### Scenario: Top VPC 侧 TLS 启用但未指定 CA 路径
- **WHEN** Top VPC NSP 启动且 `tls.enabled = true` 且 `tls.ca_cert_path` 为空
- **THEN** 系统 MUST 输出错误日志（提示需要 CA 证书路径）并终止启动

#### Scenario: Top VPC 侧 TLS 启用但未指定客户端证书路径
- **WHEN** Top VPC NSP 启动且 `tls.enabled = true` 且 `tls.cert_path` 或 `tls.key_path` 为空
- **THEN** 系统 MUST 输出错误日志（提示需要客户端证书和私钥路径用于 mTLS）并终止启动

#### Scenario: AZ VPC 侧 TLS 启用且 mode 为 process 但未指定证书路径
- **WHEN** AZ VPC NSP 启动且 `tls.enabled = true` 且 `tls.mode = "process"` 且 `tls.cert_path` 或 `tls.key_path` 为空
- **THEN** 系统 MUST 输出错误日志（提示需要证书和私钥路径）并终止启动

#### Scenario: AZ VPC 侧 mTLS 启用但未指定 CA 路径
- **WHEN** AZ VPC NSP 启动且 `tls.enabled = true` 且 `tls.client_auth = true` 且 `tls.ca_cert_path` 为空
- **THEN** 系统 MUST 输出错误日志（提示需要 CA 证书路径用于验证客户端证书）并终止启动

#### Scenario: AZ VPC 侧 TLS 启用且 mode 为 lb 时不要求证书路径
- **WHEN** AZ VPC NSP 启动且 `tls.enabled = true` 且 `tls.mode = "lb"`
- **THEN** 系统 MUST 不要求 `tls.cert_path` 和 `tls.key_path`，因为 TLS 由 LB 终止

#### Scenario: AZ VPC 侧 LB 模式且 TLS 启用但未设置 NSP_ADDR 时启动失败
- **WHEN** AZ VPC NSP 启动且 `tls.enabled = true` 且 `tls.mode = "lb"` 且 `NSP_ADDR` 环境变量未设置
- **THEN** 系统 MUST 输出错误日志（提示 LB 模式下必须通过 `NSP_ADDR` 指定 HTTPS 入口地址）并终止启动，不得静默回退到 HTTP

### Requirement: insecure_skip_verify 行为
当 `tls.insecure_skip_verify` 配置为 `true` 时，系统 SHALL 跳过对端证书的验证。此选项仅用于测试/开发环境，生产环境 MUST 保持 `false`。

#### Scenario: 测试环境跳过服务端证书验证
- **WHEN** Top VPC NSP 启动且 `tls.enabled = true` 且 `tls.insecure_skip_verify = true`
- **THEN** Top VPC 侧 `reloadableTransport` 的 `tls.Config.InsecureSkipVerify` MUST 设为 `true`，出站请求不验证 AZ 服务端证书的 CA 签名和域名

#### Scenario: insecure_skip_verify 不影响客户端证书发送
- **WHEN** `tls.insecure_skip_verify = true` 且 `tls.cert_path`/`tls.key_path` 已配置
- **THEN** Top VPC 侧仍 MUST 发送客户端证书（mTLS 握手），仅跳过对服务端证书的验证

#### Scenario: 默认关闭 insecure_skip_verify
- **WHEN** 配置文件中未指定 `tls.insecure_skip_verify`
- **THEN** 该字段 MUST 默认为 `false`，系统正常验证对端证书

### Requirement: Docker 部署证书挂载
Docker Compose 部署 SHALL 支持将证书文件通过 volume 挂载到 VPC 服务容器中。证书使用 openssl 手动生成。

#### Scenario: Docker Compose 配置证书挂载卷
- **WHEN** 使用 Docker Compose 部署且 TLS 启用
- **THEN** AZ VPC NSP 容器 MUST 能通过 volume 挂载访问到服务端证书、私钥和 CA 证书文件，Top VPC NSP 容器 MUST 能通过 volume 挂载访问到客户端证书、私钥和 CA 证书文件。VFW 服务容器不需要证书挂载。

#### Scenario: 证书文件更新不需要重建容器
- **WHEN** 宿主机上证书文件被替换
- **THEN** 容器内通过 volume 挂载的文件 MUST 同步更新，配合热更新机制实现无需重启容器即可使用新证书
