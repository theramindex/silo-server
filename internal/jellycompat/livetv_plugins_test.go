// theramindex fork: tests for the in-process livetv/v1 plugin route adapter.

package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

type fakeLiveTVPluginService struct {
	descriptors map[int][]*pluginv1.HttpRouteDescriptor
	errs        map[int]error
}

func (f *fakeLiveTVPluginService) RouteDescriptors(_ context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error) {
	if err := f.errs[installationID]; err != nil {
		return nil, err
	}
	return f.descriptors[installationID], nil
}

func (f *fakeLiveTVPluginService) HTTPRoutesClient(context.Context, int, string) (*pluginhost.HTTPRoutesClient, error) {
	return nil, nil
}

type fakeLiveTVInstallations struct {
	installs     []*plugins.Installation
	capabilities map[int][]*plugins.Capability
	capsErr      error
	listedTypes  []string
}

func (f *fakeLiveTVInstallations) ListEnabledWithCapabilityTypes(_ context.Context, types []string) ([]*plugins.Installation, error) {
	f.listedTypes = types
	return f.installs, nil
}

func (f *fakeLiveTVInstallations) ListCapabilities(_ context.Context, installationID int) ([]*plugins.Capability, error) {
	return f.capabilities[installationID], f.capsErr
}

func route(method, path string) *pluginv1.HttpRouteDescriptor {
	return &pluginv1.HttpRouteDescriptor{Method: method, Path: path}
}

func TestPluginLiveTVRoutesListsOnlyContractProviders(t *testing.T) {
	installs := &fakeLiveTVInstallations{installs: []*plugins.Installation{
		{ID: 30, PluginID: "silo.ramindex.xtream", Kind: plugins.KindPlugin},
		{ID: 5, PluginID: "silo.builtin", Kind: plugins.KindBuiltin},
		{ID: 12, PluginID: "silo.ramindex.dispatcharr", Kind: plugins.KindPlugin},
		{ID: 20, PluginID: "silo.other", Kind: plugins.KindPlugin},
		{ID: 25, PluginID: "silo.broken", Kind: plugins.KindPlugin},
		{ID: 40, PluginID: "silo.wildcard", Kind: plugins.KindPlugin},
		{ID: 41, PluginID: "silo.postonly", Kind: plugins.KindPlugin},
	}}
	svc := &fakeLiveTVPluginService{
		descriptors: map[int][]*pluginv1.HttpRouteDescriptor{
			30: {route("GET", liveTVPathChannels)},
			5:  {route("GET", liveTVPathChannels)},
			12: {route("", liveTVPathInfo), route("*", liveTVPathChannels)},
			20: {route("GET", "/api/other")},
			40: {route("GET", "/silo-compat/*")},
			41: {route("POST", liveTVPathChannels)},
		},
		errs: map[int]error{25: errors.New("boom")},
	}
	routes := &pluginLiveTVRoutes{svc: svc, installations: installs}

	got, err := routes.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	want := []LiveTVProviderRef{
		{InstallationID: 12, PluginID: "silo.ramindex.dispatcharr"},
		{InstallationID: 30, PluginID: "silo.ramindex.xtream"},
		{InstallationID: 40, PluginID: "silo.wildcard"},
	}
	if len(got) != len(want) {
		t.Fatalf("providers = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("providers[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if len(installs.listedTypes) != 1 || installs.listedTypes[0] != "http_routes.v1" {
		t.Fatalf("listed capability types = %v", installs.listedTypes)
	}
}

func TestPluginLiveTVRoutesOnlyBuiltinDeclaring(t *testing.T) {
	installs := &fakeLiveTVInstallations{installs: []*plugins.Installation{
		{ID: 5, PluginID: "silo.builtin", Kind: plugins.KindBuiltin},
	}}
	svc := &fakeLiveTVPluginService{descriptors: map[int][]*pluginv1.HttpRouteDescriptor{
		5: {route("GET", liveTVPathChannels)},
	}}
	got, err := (&pluginLiveTVRoutes{svc: svc, installations: installs}).ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("builtin installation listed: %+v", got)
	}
}

// capturingRoutes returns routes whose handle seam records the request and
// answers with resp.
func capturingRoutes(resp *pluginv1.HandleHTTPResponse, handleErr error) (*pluginLiveTVRoutes, *[]*pluginv1.HandleHTTPRequest, *[]string) {
	var reqs []*pluginv1.HandleHTTPRequest
	var capIDs []string
	routes := &pluginLiveTVRoutes{
		svc: &fakeLiveTVPluginService{},
		installations: &fakeLiveTVInstallations{capabilities: map[int][]*plugins.Capability{
			7: {
				{InstallationID: 7, Type: "metadata.v1", ID: "meta"},
				{InstallationID: 7, Type: "http_routes.v1", ID: "routes-cap"},
			},
		}},
		handle: func(_ context.Context, _ liveTVPluginService, _ int, capabilityID string, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
			reqs = append(reqs, req)
			capIDs = append(capIDs, capabilityID)
			return resp, handleErr
		},
	}
	return routes, &reqs, &capIDs
}

func TestPluginLiveTVRoutesSetsIdentityHeaders(t *testing.T) {
	routes, reqs, capIDs := capturingRoutes(&pluginv1.HandleHTTPResponse{StatusCode: 200, Body: []byte(`{}`)}, nil)

	_, _, err := routes.Call(context.Background(), 7, LiveTVCaller{UserID: 7, ProfileID: "p9"},
		http.MethodGet, liveTVPathStream, map[string]string{"channel_id": "c1", "format": "hls"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(*reqs) != 1 {
		t.Fatalf("handle calls = %d, want 1", len(*reqs))
	}
	req := (*reqs)[0]
	if (*capIDs)[0] != "routes-cap" {
		t.Fatalf("capability id = %q, want routes-cap", (*capIDs)[0])
	}
	if req.GetMethod() != http.MethodGet || req.GetPath() != liveTVPathStream {
		t.Fatalf("request = %s %s", req.GetMethod(), req.GetPath())
	}
	wantHeaders := map[string]string{"X-Silo-User-Id": "7", "X-Silo-User-Role": "user", "X-Silo-Profile-Id": "p9"}
	if len(req.GetHeaders()) != len(wantHeaders) {
		t.Fatalf("headers = %v, want %v", req.GetHeaders(), wantHeaders)
	}
	for k, v := range wantHeaders {
		if got := req.GetHeaders()[k]; got != v {
			t.Fatalf("header %s = %q, want %q", k, got, v)
		}
	}
	q := req.GetQuery().AsMap()
	if q["channel_id"] != "c1" || q["format"] != "hls" || len(q) != 2 {
		t.Fatalf("query = %v", q)
	}

	// Admins are labelled, and an empty profile id sends no profile header.
	if _, _, err := routes.Call(context.Background(), 7, LiveTVCaller{UserID: 3, Admin: true},
		http.MethodGet, liveTVPathChannels, nil); err != nil {
		t.Fatalf("Call admin: %v", err)
	}
	admin := (*reqs)[1]
	if admin.GetHeaders()["X-Silo-User-Role"] != "admin" {
		t.Fatalf("admin role header = %q", admin.GetHeaders()["X-Silo-User-Role"])
	}
	if _, ok := admin.GetHeaders()["X-Silo-Profile-Id"]; ok {
		t.Fatalf("empty profile id sent a profile header: %v", admin.GetHeaders())
	}
	if admin.GetQuery() != nil {
		t.Fatalf("empty query sent a query struct: %v", admin.GetQuery())
	}

	// Anonymous callers carry no identity headers at all.
	if _, _, err := routes.Call(context.Background(), 7, LiveTVCaller{ProfileID: "p9"},
		http.MethodGet, liveTVPathChannels, nil); err != nil {
		t.Fatalf("Call anonymous: %v", err)
	}
	for k := range (*reqs)[2].GetHeaders() {
		t.Fatalf("anonymous call sent header %s", k)
	}
}

func TestPluginLiveTVRoutesDefaultsStatus200(t *testing.T) {
	routes, _, _ := capturingRoutes(&pluginv1.HandleHTTPResponse{Body: []byte("ok")}, nil)
	status, body, err := routes.Call(context.Background(), 7, LiveTVCaller{}, http.MethodGet, liveTVPathInfo, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if status != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status, body = %d, %q", status, body)
	}

	routes, _, _ = capturingRoutes(&pluginv1.HandleHTTPResponse{StatusCode: 404, Body: []byte("nope")}, nil)
	status, _, err = routes.Call(context.Background(), 7, LiveTVCaller{}, http.MethodGet, liveTVPathInfo, nil)
	if err != nil || status != http.StatusNotFound {
		t.Fatalf("status, err = %d, %v", status, err)
	}
}

func TestPluginLiveTVRoutesCallErrors(t *testing.T) {
	handleErr := errors.New("plugin down")
	routes, _, _ := capturingRoutes(nil, handleErr)
	if _, _, err := routes.Call(context.Background(), 7, LiveTVCaller{}, http.MethodGet, liveTVPathInfo, nil); !errors.Is(err, handleErr) {
		t.Fatalf("err = %v, want %v", err, handleErr)
	}

	// No http_routes.v1 capability: the handler must not be reached.
	routes, reqs, _ := capturingRoutes(&pluginv1.HandleHTTPResponse{}, nil)
	if _, _, err := routes.Call(context.Background(), 99, LiveTVCaller{}, http.MethodGet, liveTVPathInfo, nil); err == nil {
		t.Fatal("Call without an http_routes.v1 capability succeeded")
	}
	if len(*reqs) != 0 {
		t.Fatalf("handle reached without a capability")
	}
}

func TestPluginLiveTVRoutesCallAddsDeadline(t *testing.T) {
	var gotDeadline bool
	routes, _, _ := capturingRoutes(&pluginv1.HandleHTTPResponse{}, nil)
	routes.handle = func(ctx context.Context, _ liveTVPluginService, _ int, _ string, _ *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
		_, gotDeadline = ctx.Deadline()
		return &pluginv1.HandleHTTPResponse{}, nil
	}
	if _, _, err := routes.Call(context.Background(), 7, LiveTVCaller{}, http.MethodGet, liveTVPathInfo, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !gotDeadline {
		t.Fatal("handle context has no deadline")
	}
}
