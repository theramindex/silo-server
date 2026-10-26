package plugins

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type relayTestClient struct{ target string }

func (c relayTestClient) Handle(context.Context, *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	return &pluginv1.HandleHTTPResponse{StatusCode: 200, Headers: map[string]string{"X-Silo-Relay-URL": c.target}}, nil
}

type relayTestService struct {
	client relayTestClient
	access string
}

func (s relayTestService) RouteDescriptors(context.Context, int) ([]*pluginv1.HttpRouteDescriptor, error) {
	return []*pluginv1.HttpRouteDescriptor{{Method: "GET", Path: "/media", Access: s.access}}, nil
}
func (relayTestService) ResolveAssetPath(context.Context, int, string) (string, error) {
	return "", nil
}
func (s relayTestService) HTTPRoutesClient(context.Context, int, string) (httpRouteClient, error) {
	return s.client, nil
}
func TestHTTPProxyRelaysLiveBytesAndCancelsTheProducer(t *testing.T) {
	stopped := make(chan struct{})
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(stopped)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("caller credentials reached relay producer")
		}
		if r.Header.Get("Range") != "bytes=0-11" {
			t.Error("caller range did not reach producer")
		}
		w.Header().Set("Content-Range", "bytes 0-11/99")
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Set-Cookie", "forbidden=1")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = fmt.Fprint(w, "first packet")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer producer.Close()
	proxy := NewHTTPProxy(relayTestService{client: relayTestClient{producer.URL}, access: "authenticated"}, profileTestInstallationStore{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxy.ServeRoute(w, r, 1, true, false) }))
	defer host.Close()
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", host.URL+"/api/v2/plugin-content/plugins/1/media", nil)
	req.Header.Set("Range", "bytes=0-11")
	req.Header.Set("Authorization", "Bearer caller-only")
	req.Header.Set("Cookie", "session=caller-only")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, len("first packet"))
	_, err = io.ReadFull(response.Body, data)
	_ = response.Body.Close()
	if err != nil || string(data) != "first packet" {
		t.Fatalf("live bytes did not arrive before stream completion: %q %v", data, err)
	}
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != "bytes 0-11/99" {
		t.Fatal("partial media response was not preserved")
	}
	if response.Header.Get("Set-Cookie") != "" || response.Header.Get("X-Silo-Relay-URL") != "" || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("private relay headers escaped: %v", response.Header)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("closing the player did not cancel the producer")
	}
}
func TestHTTPProxyRelayRejectsUntrustedTargetsAndPublicRoutes(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = fmt.Fprint(w, "private") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	for _, tc := range []struct {
		name, target, access string
		authenticated        bool
		want                 int
	}{
		{"no authentication", target.URL, "authenticated", false, 401},
		{"public route", target.URL, "public", true, 502},
		{"non loopback", "http://169.254.169.254/latest/meta-data", "authenticated", true, 502},
		{"DNS name", "http://localhost:8080/private", "authenticated", true, 502},
		{"file scheme", "file:///etc/passwd", "authenticated", true, 502},
		{"redirect", redirect.URL, "authenticated", true, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			proxy := NewHTTPProxy(relayTestService{client: relayTestClient{tc.target}, access: tc.access}, profileTestInstallationStore{})
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", "/api/v2/plugin-content/plugins/1/media", nil)
			proxy.ServeRoute(rec, req, 1, tc.authenticated, false)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), "private") {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatal("a refused target received a request")
	}
}
