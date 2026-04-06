// client.go - HTTP 客户端封装（出站请求自动注入）
// Package trace 提供分布式链路追踪功能
package trace

import (
	"context"
	"io"
	"net/http"
	"time"
)

// TracedClient 带追踪能力的 HTTP 客户端
// 封装标准 *http.Client，自动注入追踪信息到出站请求
type TracedClient struct {
	inner *http.Client
}

// NewTracedClient 创建带追踪能力的 HTTP 客户端
// inner 为 nil 时使用默认 http.Client（30s 超时）
func NewTracedClient(inner *http.Client) *TracedClient {
	if inner == nil {
		inner = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	return &TracedClient{inner: inner}
}

// Do 发送请求
// 自动从 req.Context() 中取出 TraceContext 并调用 Inject 注入请求头
// context 中无 TraceContext 时不注入，正常发送
func (c *TracedClient) Do(req *http.Request) (*http.Response, error) {
	// 从请求的 context 中取出 TraceContext
	tc, ok := TraceFromContext(req.Context())
	if ok && tc != nil {
		// 注入追踪信息到请求头
		Inject(req, tc)
	}

	// 发送请求
	return c.inner.Do(req)
}

// Get 封装 GET 请求
// 自动从 ctx 中取出 TraceContext 并注入请求头
func (c *TracedClient) Get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

// Post 封装 POST 请求
// 自动从 ctx 中取出 TraceContext 并注入请求头
func (c *TracedClient) Post(ctx context.Context, url string, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.Do(req)
}

// Client 返回内部的 http.Client，供需要直接访问的场景使用
func (c *TracedClient) Client() *http.Client {
	return c.inner
}
