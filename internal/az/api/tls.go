package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"workflow_qoder/internal/config"

	"github.com/jinleili-zz/nsp-platform/logger"
)

type clientCAState struct {
	pool    *x509.CertPool
	modTime time.Time
}

type clientCAReloader struct {
	path  string
	state atomic.Value
}

func newServerTLSConfig(ctx context.Context, cfg config.TLSConfig) (*tls.Config, error) {
	if _, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath); err != nil {
		return nil, fmt.Errorf("load server certificate: %w", err)
	}

	tlsConfig := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
			if err != nil {
				return nil, fmt.Errorf("load server certificate: %w", err)
			}
			return &cert, nil
		},
	}

	if !cfg.ClientAuth {
		tlsConfig.ClientAuth = tls.NoClientCert
		return tlsConfig, nil
	}

	reloader, err := newClientCAReloader(cfg.CACertPath)
	if err != nil {
		return nil, err
	}

	tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	tlsConfig.ClientCAs = reloader.currentPool()
	tlsConfig.VerifyPeerCertificate = reloader.verifyPeerCertificate
	tlsConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		clone := tlsConfig.Clone()
		clone.ClientCAs = reloader.currentPool()
		clone.GetConfigForClient = nil
		return clone, nil
	}

	interval := cfg.CAReloadInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go reloader.watch(ctx, interval)

	return tlsConfig, nil
}

func newClientCAReloader(path string) (*clientCAReloader, error) {
	state, err := loadClientCAState(path)
	if err != nil {
		return nil, err
	}

	reloader := &clientCAReloader{path: path}
	reloader.state.Store(state)
	return reloader, nil
}

func (r *clientCAReloader) currentPool() *x509.CertPool {
	state, _ := r.state.Load().(clientCAState)
	return state.pool
}

func (r *clientCAReloader) verifyPeerCertificate(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("client certificate is required")
	}

	pool := r.currentPool()
	if pool == nil {
		return fmt.Errorf("client CA pool is not initialized")
	}

	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("parse client certificate: %w", err)
	}

	intermediates := x509.NewCertPool()
	for _, raw := range rawCerts[1:] {
		cert, err := x509.ParseCertificate(raw)
		if err != nil {
			return fmt.Errorf("parse client intermediate certificate: %w", err)
		}
		intermediates.AddCert(cert)
	}

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         pool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return fmt.Errorf("verify client certificate: %w", err)
	}

	return nil
}

func (r *clientCAReloader) watch(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, _ := r.state.Load().(clientCAState)
			info, err := os.Stat(r.path)
			if err != nil {
				logger.Platform().Warn("检查客户端 CA 文件失败", "path", r.path, "error", err)
				continue
			}
			if info.ModTime().Equal(state.modTime) {
				continue
			}

			nextState, err := loadClientCAState(r.path)
			if err != nil {
				logger.Platform().Warn("重载客户端 CA 失败，继续使用旧 CA", "path", r.path, "error", err)
				continue
			}
			r.state.Store(nextState)
			logger.Platform().Info("客户端 CA 池已热更新", "path", r.path)
		}
	}
}

func loadClientCAState(path string) (clientCAState, error) {
	info, err := os.Stat(path)
	if err != nil {
		return clientCAState{}, fmt.Errorf("stat client CA file %q: %w", path, err)
	}

	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return clientCAState{}, fmt.Errorf("read client CA file %q: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return clientCAState{}, fmt.Errorf("append client CA file %q: invalid PEM", path)
	}

	return clientCAState{
		pool:    pool,
		modTime: info.ModTime(),
	}, nil
}
