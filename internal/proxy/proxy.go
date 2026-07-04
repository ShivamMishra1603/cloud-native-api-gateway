package proxy

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// ProxyHandler wraps httputil.ReverseProxy to forward traffic to an upstream service.
type ProxyHandler struct {
	target *url.URL
	proxy  *httputil.ReverseProxy
}

// New creates a new ProxyHandler targeting the specified URL.
func New(targetURL string) (*ProxyHandler, error) {
	target, err := url.Parse(targetURL)
	if err != nil {
		return nil, err
	}

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			// Rewrite scheme, host, and incoming host header
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host

			// Merge raw queries correctly
			targetQuery := target.RawQuery
			if targetQuery == "" || req.URL.RawQuery == "" {
				req.URL.RawQuery = targetQuery + req.URL.RawQuery
			} else {
				req.URL.RawQuery = targetQuery + "&" + req.URL.RawQuery
			}

			// Clean and join URLs
			req.URL.Path = singleJoiningSlash(target.Path, req.URL.Path)

			// Propagate client IP in X-Forwarded-For header
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				if prior, ok := req.Header["X-Forwarded-For"]; ok {
					clientIP = strings.Join(prior, ", ") + ", " + clientIP
				}
				req.Header.Set("X-Forwarded-For", clientIP)
			}

			slog.Debug("request proxied",
				"method", req.Method,
				"path", req.URL.Path,
				"query", req.URL.RawQuery,
				"target", target.String(),
			)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("upstream connection error",
				"error", err,
				"path", r.URL.Path,
				"target", target.String(),
			)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("502 Bad Gateway - Upstream service is unreachable\n"))
		},
	}

	return &ProxyHandler{
		target: target,
		proxy:  rp,
	}, nil
}

// ServeHTTP forwards incoming HTTP requests using the reverse proxy.
func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.proxy.ServeHTTP(w, r)
}

// singleJoiningSlash safely joins target base path and request path
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}
