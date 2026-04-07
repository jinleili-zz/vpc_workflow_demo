## ADDED Requirements

### Requirement: AK/SK配置加载

系统SHALL支持从YAML配置文件加载AK/SK凭证。

#### Scenario: 成功加载配置文件中的AK/SK
- **WHEN** 服务启动时读取配置文件
- **THEN** 系统解析auth.credentials字段并初始化CredentialStore

#### Scenario: 支持环境变量注入
- **WHEN** 配置文件中的secret_key值为环境变量引用格式（如`${TOP_NSP_SK}`）
- **THEN** 系统自动解析并替换为对应环境变量的值

### Requirement: 全局凭证配置

系统SHALL支持全局统一的AK/SK凭证配置。

#### Scenario: 单一AK/SK凭证
- **WHEN** 配置文件包含单个凭证配置
- **THEN** Top-NSP和AZ-NSP使用相同的AK/SK进行签名和验证

#### Scenario: 凭证启用状态
- **WHEN** 配置中enabled字段为false
- **THEN** 该凭证SHALL NOT被用于签名或验证
