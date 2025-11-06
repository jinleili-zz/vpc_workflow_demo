# Tyk API Gateway 认证配置指南

## 🔐 支持的认证机制

### 1. Auth Token (API Key) 认证

**适用场景**: 服务间调用、后台管理接口

**配置文件**: `apps/nsp-top-api.json`

**关键配置**:
```json
{
  "use_keyless": false,
  "use_standard_auth": true,
  "auth": {
    "auth_header_name": "X-API-Key"
  }
}
```

**使用方式**:
```bash
# 创建 API Key
curl -X POST http://localhost:9696/tyk/keys/my-api-key \
  -H "x-tyk-authorization: nsp-tyk-secret-key-2025" \
  -H "Content-Type: application/json" \
  -d '{
    "allowance": 1000,
    "rate": 1000,
    "per": 60,
    "access_rights": {
      "nsp-top-api-v1": {
        "api_id": "nsp-top-api-v1",
        "versions": ["Default"]
      }
    }
  }'

# 使用 API Key 访问
curl -H "X-API-Key: my-api-key" http://localhost:9696/api/v1/health
```

---

### 2. JWT (JSON Web Token) 认证

**适用场景**: 用户登录、移动应用、前后端分离

**配置文件**: `apps/nsp-top-api-jwt.json`

**关键配置**:
```json
{
  "enable_jwt": true,
  "jwt_signing_method": "hmac",
  "jwt_source": "base64_encoded_secret",
  "jwt_identity_base_field": "sub",
  "jwt_default_policies": ["default"]
}
```

**JWT Payload 示例**:
```json
{
  "sub": "user123",           // 用户ID
  "pol": "default",           // 策略ID
  "exp": 1730937600,          // 过期时间
  "iat": 1730934000,          // 签发时间
  "custom_field": "value"     // 自定义字段
}
```

**使用方式**:
```bash
# 生成 JWT (使用 Python/Node.js 等)
# 然后在请求中携带
curl -H "Authorization: Bearer eyJhbGc..." http://localhost:9696/jwt/api/v1/health
```

---

### 3. OAuth 2.0 认证

**适用场景**: 第三方集成、授权代理

**配置文件**: `apps/nsp-top-api-oauth.json`

**支持的授权类型**:
- Authorization Code (授权码模式)
- Client Credentials (客户端凭证模式)
- Refresh Token (刷新令牌)

**OAuth 流程**:
1. 客户端注册获取 client_id 和 client_secret
2. 用户授权重定向到授权页面
3. 获取授权码 (code)
4. 使用授权码交换 access_token
5. 使用 access_token 访问 API

---

## 🛠️ 配置步骤

### 步骤1: 选择认证方式

修改 `docker-compose.yml`，挂载对应的配置文件：

```yaml
tyk-gateway:
  volumes:
    - ./tyk-config/apps:/opt/tyk-gateway/apps
```

### 步骤2: 重启 Tyk Gateway

```bash
cd deployments/docker
docker-compose restart tyk-gateway
```

### 步骤3: 验证配置

```bash
# 查看加载的 API
docker-compose logs tyk-gateway | grep "API Loaded"
```

---

## 🧪 测试方法

### 自动化测试脚本

```bash
cd deployments/docker
./test-tyk-auth.sh
```

该脚本会自动测试:
- ✅ 无认证访问拒绝
- ✅ API Key 创建和使用
- ✅ 速率限制验证
- ✅ JWT 认证
- ✅ 密钥管理

### 手动测试

**1. 测试无认证访问**:
```bash
curl http://localhost:9696/api/v1/health
# 预期: 401 Unauthorized
```

**2. 创建 API Key**:
```bash
curl -X POST http://localhost:9696/tyk/keys/create \
  -H "x-tyk-authorization: nsp-tyk-secret-key-2025" \
  -H "Content-Type: application/json" \
  -d '{
    "access_rights": {
      "nsp-top-api-v1": {
        "api_id": "nsp-top-api-v1",
        "versions": ["Default"]
      }
    }
  }'
```

**3. 使用 API Key 访问**:
```bash
curl -H "X-API-Key: YOUR_KEY_HERE" http://localhost:9696/api/v1/health
# 预期: 200 OK
```

---

## 🔒 安全建议

### 1. 密钥管理
- ✅ 定期轮换 API Key
- ✅ 为不同环境使用不同的密钥
- ✅ 限制密钥的访问权限和速率
- ✅ 记录密钥使用情况

### 2. JWT 配置
- ✅ 使用强密钥 (至少256位)
- ✅ 设置合理的过期时间 (1-24小时)
- ✅ 启用令牌刷新机制
- ✅ 验证 JWT 签名算法

### 3. 速率限制
```json
{
  "rate": 1000,        // 每分钟1000次
  "per": 60,           // 时间窗口60秒
  "quota_max": 10000   // 每小时配额
}
```

### 4. IP 白名单
```json
{
  "enable_ip_whitelisting": true,
  "allowed_ips": [
    "192.168.1.0/24",
    "10.0.0.0/8"
  ]
}
```

---

## 📊 监控和日志

### 查看认证日志
```bash
docker-compose logs tyk-gateway | grep -i "auth\|denied\|unauthorized"
```

### 查看 Redis 中的密钥
```bash
docker exec nsp-redis redis-cli -n 2 KEYS "apikey-*"
```

### 查看速率限制状态
```bash
docker exec nsp-redis redis-cli -n 2 ZRANGE "nsp-top-api-v1.Request" 0 -1 WITHSCORES
```

---

## 🚀 生产环境部署清单

- [ ] 禁用 keyless 模式
- [ ] 启用 HTTPS (TLS 终止)
- [ ] 配置密钥自动过期
- [ ] 启用详细日志记录
- [ ] 设置合理的速率限制
- [ ] 配置 IP 白名单/黑名单
- [ ] 启用分析和监控
- [ ] 定期备份 Redis 数据
- [ ] 配置告警规则
- [ ] 文档化密钥管理流程
