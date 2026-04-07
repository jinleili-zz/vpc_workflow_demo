package client

import (
	"fmt"
	"io"
	"net/http"

	"github.com/jinleili-zz/nsp-platform/auth"
	"github.com/jinleili-zz/nsp-platform/trace"
)

// SignedTracedClient combines trace propagation and AK/SK request signing.
type SignedTracedClient struct {
	tracedClient *trace.TracedClient
	signer       *auth.Signer
}

func NewSignedTracedClient(tracedClient *trace.TracedClient, signer *auth.Signer) *SignedTracedClient {
	if tracedClient == nil {
		tracedClient = trace.NewTracedClient(nil)
	}
	return &SignedTracedClient{
		tracedClient: tracedClient,
		signer:       signer,
	}
}

func (c *SignedTracedClient) Do(req *http.Request) (*http.Response, error) {
	if c.signer != nil {
		if err := c.signer.Sign(req); err != nil {
			return nil, fmt.Errorf("sign request: %w", err)
		}
	}
	return c.tracedClient.Do(req)
}

func (c *SignedTracedClient) Get(req *http.Request) (*http.Response, error) {
	return c.Do(req)
}

func (c *SignedTracedClient) Post(req *http.Request) (*http.Response, error) {
	return c.Do(req)
}

func (c *SignedTracedClient) NewRequest(method, url string, body io.Reader) (*http.Request, error) {
	return http.NewRequest(method, url, body)
}
