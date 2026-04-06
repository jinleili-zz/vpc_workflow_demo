# 配置管理模块实现说明

## 实现概述

已在 `nsp_platform/nsp-common/pkg/config/` 目录下实现了统一的配置管理 SDK，封装了 `github.com/spf13/viper` 作为底层实现。

## 文件结构

```
nsp-common/
└── pkg/
    └── config/
        ├── config.go        # 对外接口定义
        ├── viper.go         # viper 实现
        └── config_test.go   # 单元测试
```

## 核心组件

### 1. 接口层 (`config.go`)

- **Loader 接口**: 业务代码只依赖此接口
  - `Load(target any) error` - 加载并反序列化配置
  - `OnChange(fn func(apply func(any) error))` - 注册配置变更回调
  - `Close()` - 停止监听并释放资源（可多次调用）

- **Option 结构体**: 配置选项
  - `ConfigFile` - 配置文件完整路径
  - `ConfigName` + `ConfigPaths` - 配置文件名和搜索路径
  - `ConfigType` - 配置格式（yaml/json/toml）
  - `Defaults` - 默认值映射
  - `Watch` - 是否开启热更新
  - `EnvPrefix` - 环境变量前缀

### 2. 实现层 (`viper.go`)

- **viperLoader 结构体**: viper 的封装实现
  - 内部使用 `*viper.Viper` 实例
  - `sync.RWMutex` 保护回调列表和 viper 内部状态的并发访问
  - `callbacks []func(func(any) error)` 存储注册的回调
  - `done chan struct{}` 用于 Close 信号
  - `closeOnce sync.Once` 保证 Close 幂等
  - `watchOnce sync.Once` 保证 watcher 只启动一次

- **关键特性**:
  - 严格模式：`Load()` 和热更新回调均使用 `UnmarshalExact`，未知字段报错
  - 延迟启动 watcher：首次 `Load()` 成功后才启动文件监听，避免与初始化竞争
  - 并发安全：`Load()` 和热更新回调共享 `mu` 锁，防止 viper 内部状态竞争
  - 热更新支持：使用 viper.WatchConfig() 和 OnConfigChange()
  - panic 隔离：对每个回调单独 recover，一个回调崩溃不阻塞后续回调
  - 回调快照（copy-before-execute）：持锁期间仅拷贝回调列表，执行阶段不持锁
  - Kubernetes 兼容：fsnotify 已处理软链接原子替换场景
  - 资源管理：Close() 后不再触发回调，支持多次调用

### 3. 测试覆盖 (`config_test.go`)

实现了 11 个测试用例，覆盖：

1. `Test_Load_YAML` - YAML 格式加载
2. `Test_Load_JSON` - JSON 格式加载
3. `Test_Load_DefaultValue` - 默认值使用
4. `Test_Load_EnvOverride` - 环境变量覆盖
5. `Test_Load_UnknownField` - 未知字段检测（严格模式）
6. `Test_Load_FileNotFound` - 文件不存在错误处理
7. `Test_OnChange_Triggered` - 热更新回调触发
8. `Test_OnChange_MultipleCallbacks` - 多回调按序执行
9. `Test_OnChange_WatchFalse` - Watch 关闭时回调注册
10. `Test_Close_StopsWatch` - Close 停止监听
11. `Test_OnChange_PanicRecovery` - 回调 panic 恢复

## 依赖要求

需要在 `go.mod` 中添加以下依赖：

```go
require (
    github.com/spf13/viper v1.21.0
    github.com/fsnotify/fsnotify v1.9.0
)
```

**注意**: 这些依赖已在当前 go.mod 中存在，无需额外添加。

## 使用示例

```go
type ServerConfig struct {
    Host string `mapstructure:"host"`
    Port int    `mapstructure:"port"`
}

type AppConfig struct {
    Server ServerConfig `mapstructure:"server"`
    Debug  bool         `mapstructure:"debug"`
}

func main() {
    loader, err := config.New(config.Option{
        ConfigFile: "./config/config.yaml",
        Watch:      true,
        EnvPrefix:  "NSP",
        Defaults: map[string]any{
            "server.port": 8080,
            "debug":       false,
        },
    })
    if err != nil {
        panic(err)
    }
    defer loader.Close()

    var cfg AppConfig
    if err := loader.Load(&cfg); err != nil {
        panic(err)
    }

    // 注册热更新回调
    loader.OnChange(func(apply func(target any) error) {
        var newCfg AppConfig
        if err := apply(&newCfg); err != nil {
            // 新配置解析失败，记录日志，继续使用旧配置
            return
        }
        cfg = newCfg
    })

    // 业务代码使用 cfg，不直接调用任何 viper API
}
```

## 设计亮点

1. **接口隔离**: 业务代码只依赖接口，实现层可替换
2. **严格模式**: Load 和热更新均使用 UnmarshalExact，未知字段报错，防止配置拼写错误
3. **并发安全**: Load 和热更新共享互斥锁，watcher 延迟到首次 Load 后启动
4. **错误恢复**: 单个回调 panic 不影响其他回调执行
5. **幂等关闭**: Close() 使用 sync.Once 保护，可安全多次调用
6. **K8s 兼容**: 正确处理 Kubernetes Secret 卷更新场景
7. **测试完备**: 11 个测试用例覆盖主要使用场景
