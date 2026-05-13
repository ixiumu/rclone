package fshttp

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"

	"github.com/rclone/rclone/fs"
)

type CustomOptions struct {
	Server   string
	Insecure bool
	Proxy    string
	RelayURL string
}

type RelayRoundTripper struct {
	Base     http.RoundTripper
	RelayURL *url.URL
}

func (rt *RelayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clonedReq := req.Clone(req.Context())

	encodedUrl := base64.StdEncoding.EncodeToString([]byte(req.URL.String()))
	clonedReq.Header.Set("X-Original-Url-B64", encodedUrl)
	clonedReq.URL.Scheme = rt.RelayURL.Scheme
	clonedReq.URL.Host = rt.RelayURL.Host
	clonedReq.Host = rt.RelayURL.Host

	return rt.Base.RoundTrip(clonedReq)
}

func NewClientWithCustomOptions(ctx context.Context, opts *CustomOptions) *http.Client {
	if opts == nil {
		return NewClient(ctx)
	}

	customize := func(t *http.Transport) {
		if opts.Insecure {
			if t.TLSClientConfig == nil {
				t.TLSClientConfig = &tls.Config{}
			}
			t.TLSClientConfig.InsecureSkipVerify = true
		}

		if opts.Proxy != "" {
			if pURL, err := url.Parse(opts.Proxy); err == nil {
				t.Proxy = http.ProxyURL(pURL)
			} else {
				fs.Errorf(nil, "fshttp: invalid custom proxy URL %q: %v", opts.Proxy, err)
			}
		}

		if opts.Server != "" {
			baseDialer := NewDialer(ctx)
			t.DialContext = func(reqCtx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					port = "443"
				}
				targetAddr := net.JoinHostPort(opts.Server, port)
				return baseDialer.DialContext(reqCtx, network, targetAddr)
			}
		}
	}

	client := NewClientCustom(ctx, customize)

	if opts.RelayURL != "" {
		if rURL, err := url.Parse(opts.RelayURL); err == nil {
			client.Transport = &RelayRoundTripper{
				Base:     client.Transport,
				RelayURL: rURL,
			}
		} else {
			fs.Errorf(nil, "fshttp: invalid custom relay URL %q: %v", opts.RelayURL, err)
		}
	}

	return client
}
