package bootstrap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/jinleili-zz/nsp-platform/logger"
)

type reloadableTransport struct {
	current atomic.Value
}

func newReloadableTransport(transport *http.Transport) *reloadableTransport {
	rt := &reloadableTransport{}
	rt.current.Store(transport)
	return rt
}

func (r *reloadableTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport, _ := r.current.Load().(*http.Transport)
	if transport == nil {
		return nil, fmt.Errorf("reloadable transport is not initialized")
	}
	return transport.RoundTrip(req)
}

func (r *reloadableTransport) swap(next *http.Transport) {
	prev, _ := r.current.Load().(*http.Transport)
	r.current.Store(next)
	if prev != nil {
		prev.CloseIdleConnections()
	}
}

func newReloadableMTLSHTTPClient(ctx context.Context, cfg TLSClientConfig) (*http.Client, error) {
	transport, err := buildMTLSTransport(cfg)
	if err != nil {
		return nil, err
	}

	reloadable := newReloadableTransport(transport)
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: reloadable,
	}

	interval := cfg.CAReloadInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}

	files := []string{cfg.CACertPath, cfg.CertPath, cfg.KeyPath}
	modTimes, err := readFileModTimes(files)
	if err != nil {
		return nil, err
	}

	go watchMTLSFiles(ctx, interval, cfg, reloadable, modTimes)
	return client, nil
}

func watchMTLSFiles(ctx context.Context, interval time.Duration, cfg TLSClientConfig, transport *reloadableTransport, modTimes map[string]time.Time) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nextModTimes, changed, err := detectFileChanges(modTimes)
			if err != nil {
				logger.Platform().Warn("检查 TLS 文件变更失败", "error", err)
				continue
			}
			if !changed {
				continue
			}

			nextTransport, err := buildMTLSTransport(cfg)
			if err != nil {
				logger.Platform().Warn("重载 mTLS transport 失败，继续使用旧配置", "error", err)
				continue
			}

			transport.swap(nextTransport)
			modTimes = nextModTimes
			logger.Platform().Info("mTLS transport 已热更新")
		}
	}
}

func buildMTLSTransport(cfg TLSClientConfig) (*http.Transport, error) {
	rootCAs, err := loadCertPool(cfg.CACertPath)
	if err != nil {
		return nil, err
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}

	baseTransport, _ := http.DefaultTransport.(*http.Transport)
	transport := baseTransport.Clone()
	transport.TLSClientConfig = &tls.Config{
		RootCAs:            rootCAs,
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	return transport, nil
}

func loadCertPool(path string) (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA certificate %q: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("append CA certificate %q: invalid PEM", path)
	}
	return pool, nil
}

func readFileModTimes(paths []string) (map[string]time.Time, error) {
	modTimes := make(map[string]time.Time, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat TLS file %q: %w", path, err)
		}
		modTimes[path] = info.ModTime()
	}
	return modTimes, nil
}

func detectFileChanges(modTimes map[string]time.Time) (map[string]time.Time, bool, error) {
	next := make(map[string]time.Time, len(modTimes))
	changed := false
	for path, modTime := range modTimes {
		info, err := os.Stat(path)
		if err != nil {
			return nil, false, fmt.Errorf("stat TLS file %q: %w", path, err)
		}
		next[path] = info.ModTime()
		if !info.ModTime().Equal(modTime) {
			changed = true
		}
	}
	return next, changed, nil
}
