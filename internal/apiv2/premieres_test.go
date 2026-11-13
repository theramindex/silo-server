package apiv2

import (
	"net/http"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

// A configured service with no DB makes an authorization regression fail at
// the business boundary, rather than being hidden by an unavailable service.
func TestPremiereProfileAuthority(t *testing.T) {
	deps := pilotDeps(nil, nil)
	deps.Premieres = notifications.NewPremiereService(nil, nil, nil)
	h := NewHandler(deps)
	for _, path := range []string{"/premieres", "/premieres/settings"} {
		for _, tc := range []struct {
			headers map[string]string
			problem ProblemType
		}{
			{nil, TypeAuthenticationRequired},
			{bearer(memberToken), TypeValidationFailed},
			{with(bearer(memberToken), "X-Profile-Id", "p-foreign"), TypeNotFound},
		} {
			requireProblem(t, do(t, h, http.MethodGet, Prefix+path, "", tc.headers), tc.problem)
		}
	}
	// Recipient identity must not be supplied through this profile-scoped body.
	requireProblem(t, do(t, h, http.MethodPut, Prefix+"/premieres/settings", `{"user_id":2,"profile_id":"p-foreign","preferences":{"enabled":true,"window_minutes":60},"mappings":[]}`, profileOwner()), TypeValidationFailed)
}
