## ADDED Requirements

### Requirement: Saga引擎签名支持

Saga引擎SHALL支持为步骤执行的HTTP请求添加AK/SK签名。

#### Scenario: CredentialStore配置
- **WHEN** 初始化Saga Engine时传入CredentialStore
- **THEN** Engine能够解析Step.AuthAK对应的SecretKey

#### Scenario: Action请求签名
- **WHEN** Saga执行Step的Action HTTP请求且Step.AuthAK非空
- **THEN** 请求自动添加AK/SK签名

#### Scenario: Compensate请求签名
- **WHEN** Saga执行Step的Compensate HTTP请求且Step.AuthAK非空
- **THEN** 请求自动添加AK/SK签名

#### Scenario: Poll请求签名
- **WHEN** Saga执行异步Step的Poll HTTP请求且Step.AuthAK非空
- **THEN** 请求自动添加AK/SK签名

### Requirement: Step级别AuthAK配置

Saga Step SHALL支持配置AuthAK字段指定签名使用的AccessKey。

#### Scenario: VPC创建Saga签名
- **WHEN** 构建VPC创建Saga Step时设置AuthAK为"top-nsp"
- **THEN** 该步骤的Action和Compensate请求使用top-nsp的SK签名

#### Scenario: PCCN创建Saga签名
- **WHEN** 构建PCCN创建Saga Step时设置AuthAK为"top-nsp"
- **THEN** 该步骤的Action和Compensate请求使用top-nsp的SK签名

#### Scenario: 无AuthAK步骤
- **WHEN** Saga Step的AuthAK为空
- **THEN** 该步骤的HTTP请求不添加签名

### Requirement: 签名失败处理

Saga引擎SHALL正确处理签名失败的情况。

#### Scenario: AuthAK无效
- **WHEN** Step.AuthAK指定的AccessKey不存在于CredentialStore
- **THEN** Saga提交时返回验证错误

#### Scenario: 签名请求失败
- **WHEN** 签名后的请求被AZ-NSP拒绝（401）
- **THEN** Saga步骤标记为失败并触发补偿流程
