// generator.go - TraceID / SpanID 生成
// Package trace 提供分布式链路追踪功能
package trace

import (
	"crypto/rand"
	"encoding/hex"
	mathrand "math/rand"
	"os"
	"sync"
	"time"
)

var (
	fallbackRandOnce sync.Once
)

// initFallbackRand initializes math/rand with a time-based seed for fallback.
func initFallbackRand() {
	fallbackRandOnce.Do(func() {
		mathrand.Seed(time.Now().UnixNano())
	})
}

// NewTraceID 生成 32 位 hex 字符串的 TraceID（16字节随机数）
// 格式与 B3 标准一致（128bit）
// 示例：4bf92f3577b34da6a3ce929d0e0e4736
func NewTraceID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to math/rand if crypto/rand fails (e.g., /dev/random exhausted)
		// This is acceptable for trace IDs which don't require cryptographic security
		initFallbackRand()
		for i := range b {
			b[i] = byte(mathrand.Intn(256))
		}
	}
	return hex.EncodeToString(b)
}

// NewSpanId 生成 16 位 hex 字符串的 SpanId（8字节随机数）
// 格式与 B3 标准一致（64bit）
// 示例：00f067aa0ba902b7
func NewSpanId() string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to math/rand if crypto/rand fails
		initFallbackRand()
		for i := range b {
			b[i] = byte(mathrand.Intn(256))
		}
	}
	return hex.EncodeToString(b)
}

// GetInstanceId 读取当前实例标识
// 优先读取环境变量 HOSTNAME（k8s pod 名称）
// HOSTNAME 为空时 fallback 到 os.Hostname()
// 两者都失败时返回 "unknown"
func GetInstanceId() string {
	// 优先读取 HOSTNAME 环境变量
	hostname := os.Getenv("HOSTNAME")
	if hostname != "" {
		return hostname
	}

	// fallback 到 os.Hostname()
	hostname, err := os.Hostname()
	if err == nil && hostname != "" {
		return hostname
	}

	// 都失败时返回 unknown
	return "unknown"
}
