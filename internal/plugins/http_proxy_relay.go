package plugins

import (
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// A plugin can answer an authenticated GET/HEAD route with X-Silo-Relay-URL
// instead of buffering an unbounded media body in a gRPC response. The target
// is a literal loopback listener owned by the local plugin process. Plugins
// remain trusted installed code; this is never an arbitrary-URL client API.
// No credentials or caller-selected headers cross the loopback connection.
// Redirects are refused so the admission cannot be escaped after validation.
var pluginRelayClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ResponseHeaderTimeout: 15 * time.Second, IdleConnTimeout: 30 * time.Second, MaxIdleConns: 16},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func pluginRelayTarget(headers map[string]string) string {
	for name, value := range headers {
		if strings.EqualFold(name, "X-Silo-Relay-URL") {
			return value
		}
	}
	return ""
}
func servePluginRelay(w http.ResponseWriter, r *http.Request, target, access string, authenticated, admin bool) {
	refuse := func() { http.Error(w, "plugin stream unavailable", http.StatusBadGateway) }
	if (!authenticated && !admin) || (access != "authenticated" && access != "admin") || (r.Method != http.MethodGet && r.Method != http.MethodHead) || len(target) > 8192 {
		refuse()
		return
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" || u.Port() == "" {
		refuse()
		return
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		refuse()
		return
	}
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), nil)
	if err != nil {
		refuse()
		return
	}
	if value := r.Header.Get("Range"); value != "" {
		upstream.Header.Set("Range", value)
	}
	response, err := pluginRelayClient.Do(upstream)
	if err != nil {
		refuse()
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if response.StatusCode == http.StatusNotFound {
			http.NotFound(w, r)
		} else if response.StatusCode == http.StatusServiceUnavailable {
			http.Error(w, "plugin stream temporarily unavailable", 503)
		} else {
			refuse()
		}
		return
	}
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Silo-Stream-Relay", "1")
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.CopyBuffer(pluginRelayWriter{w}, response.Body, make([]byte, 32<<10))
}

type pluginRelayWriter struct{ http.ResponseWriter }

func (w pluginRelayWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	if flush, ok := w.ResponseWriter.(http.Flusher); ok {
		flush.Flush()
	}
	return n, err
}
