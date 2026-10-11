// theramindex fork: calls livetv/v1 plugin HTTP routes in-process.

package jellycompat

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/pluginhost"
	"github.com/Silo-Server/silo-server/internal/plugins"
)

// httpRoutesCapabilityType is the capability type of plugins that serve HTTP
// routes.
const httpRoutesCapabilityType = "http_routes.v1"

// liveTVCallTimeout bounds a plugin route call whose context has no deadline.
const liveTVCallTimeout = 10 * time.Second

// *plugins.UserConfigStore stores the per-user settings the bridge reads and
// writes.
var _ LiveTVPrefsStore = (*plugins.UserConfigStore)(nil)

// liveTVPluginService is the part of *plugins.Service the adapter uses.
type liveTVPluginService interface {
	RouteDescriptors(ctx context.Context, installationID int) ([]*pluginv1.HttpRouteDescriptor, error)
	HTTPRoutesClient(ctx context.Context, installationID int, capabilityID string) (*pluginhost.HTTPRoutesClient, error)
}

// liveTVInstallationStore is the part of *plugins.InstallationStore the
// adapter uses.
type liveTVInstallationStore interface {
	ListEnabledWithCapabilityTypes(ctx context.Context, capabilityTypes []string) ([]*plugins.Installation, error)
	ListCapabilities(ctx context.Context, installationID int) ([]*plugins.Capability, error)
}

// pluginLiveTVRoutes is the LiveTVRouteCaller backed by the plugin host.
type pluginLiveTVRoutes struct {
	svc           liveTVPluginService
	installations liveTVInstallationStore
	// handle sends one request to a plugin's http_routes capability. It is a
	// field because *pluginhost.HTTPRoutesClient cannot be faked in tests.
	handle func(ctx context.Context, svc liveTVPluginService, installationID int, capabilityID string, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error)
}

// NewPluginLiveTVRoutes returns a LiveTVRouteCaller that discovers livetv/v1
// providers among the enabled plugins and calls their routes in-process.
func NewPluginLiveTVRoutes(svc *plugins.Service, installations *plugins.InstallationStore) LiveTVRouteCaller {
	return &pluginLiveTVRoutes{svc: svc, installations: installations, handle: handlePluginRoute}
}

func handlePluginRoute(ctx context.Context, svc liveTVPluginService, installationID int, capabilityID string, req *pluginv1.HandleHTTPRequest) (*pluginv1.HandleHTTPResponse, error) {
	client, err := svc.HTTPRoutesClient(ctx, installationID, capabilityID)
	if err != nil {
		return nil, err
	}
	return client.Handle(ctx, req)
}

// ListProviders returns the enabled, non-builtin plugins that serve the livetv/v1
// channels route, ordered by installation ID.
func (r *pluginLiveTVRoutes) ListProviders(ctx context.Context) ([]LiveTVProviderRef, error) {
	installs, err := r.installations.ListEnabledWithCapabilityTypes(ctx, []string{httpRoutesCapabilityType})
	if err != nil {
		return nil, fmt.Errorf("list plugins with http routes: %w", err)
	}
	var refs []LiveTVProviderRef
	for _, inst := range installs {
		if inst == nil || inst.IsBuiltin() {
			continue
		}
		descriptors, err := r.svc.RouteDescriptors(ctx, inst.ID)
		if err != nil {
			slog.WarnContext(ctx, "jellycompat: livetv route discovery failed; skipping plugin",
				"component", "jellycompat", "installation_id", inst.ID, "error", err)
			continue
		}
		if !servesLiveTVChannels(descriptors) {
			continue
		}
		refs = append(refs, LiveTVProviderRef{InstallationID: inst.ID, PluginID: inst.PluginID})
	}
	slices.SortFunc(refs, func(a, b LiveTVProviderRef) int { return a.InstallationID - b.InstallationID })
	return refs, nil
}

// servesLiveTVChannels reports whether a descriptor answers GET on the
// livetv/v1 channels route, by exact path or "/*" wildcard prefix. Matching
// mirrors plugins.HTTPProxy. Descriptor access is deliberately ignored: the
// bridge calls contract routes in-process and is their authorizer, and
// providers declare stream and info admin-only to keep them off the public
// proxy.
func servesLiveTVChannels(descriptors []*pluginv1.HttpRouteDescriptor) bool {
	for _, d := range descriptors {
		if m := d.GetMethod(); m != "" && m != "*" && m != http.MethodGet {
			continue
		}
		p := d.GetPath()
		if p == liveTVPathChannels {
			return true
		}
		if prefix, ok := strings.CutSuffix(p, "/*"); ok &&
			(liveTVPathChannels == prefix || strings.HasPrefix(liveTVPathChannels, prefix+"/")) {
			return true
		}
	}
	return false
}

// Call sends one request to an installation's plugin routes. Callers with a
// user id carry it, with their role and profile, as X-Silo-* headers, the same
// identity plugins get through the HTTP proxy. A zero status becomes 200.
// The route's descriptor access is not enforced here: the bridge has already
// authorized the caller, and contract stream and info routes are admin-only so
// that only this in-process path can reach them for ordinary users.
func (r *pluginLiveTVRoutes) Call(ctx context.Context, installationID int, caller LiveTVCaller, method, path string, query map[string]string) (int, []byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, liveTVCallTimeout)
		defer cancel()
	}

	capabilityID, err := r.routesCapabilityID(ctx, installationID)
	if err != nil {
		return 0, nil, err
	}
	req := &pluginv1.HandleHTTPRequest{Method: method, Path: path, Headers: liveTVIdentityHeaders(caller)}
	if len(query) > 0 {
		payload := make(map[string]any, len(query))
		for k, v := range query {
			payload[k] = v
		}
		if req.Query, err = structpb.NewStruct(payload); err != nil {
			return 0, nil, fmt.Errorf("encode query: %w", err)
		}
	}

	resp, err := r.handle(ctx, r.svc, installationID, capabilityID, req)
	if err != nil {
		return 0, nil, err
	}
	status := int(resp.GetStatusCode())
	if status == 0 {
		status = http.StatusOK
	}
	return status, resp.GetBody(), nil
}

func (r *pluginLiveTVRoutes) routesCapabilityID(ctx context.Context, installationID int) (string, error) {
	capabilities, err := r.installations.ListCapabilities(ctx, installationID)
	if err != nil {
		return "", fmt.Errorf("list capabilities: %w", err)
	}
	for _, c := range capabilities {
		if c != nil && c.Type == httpRoutesCapabilityType {
			return c.ID, nil
		}
	}
	return "", fmt.Errorf("installation %d has no %s capability", installationID, httpRoutesCapabilityType)
}

func liveTVIdentityHeaders(caller LiveTVCaller) map[string]string {
	headers := map[string]string{}
	if caller.UserID <= 0 {
		return headers
	}
	headers["X-Silo-User-Id"] = strconv.Itoa(caller.UserID)
	if caller.Admin {
		headers["X-Silo-User-Role"] = "admin"
	} else {
		headers["X-Silo-User-Role"] = "user"
	}
	if caller.ProfileID != "" {
		headers["X-Silo-Profile-Id"] = caller.ProfileID
	}
	return headers
}
