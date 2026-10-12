// theramindex fork: SILO_JELLYCOMPAT_LIVETV off switch for the Live TV bridge.

package jellycompat

import (
	"slices"
	"strings"
)

// liveTVEnvVar turns the Live TV bridge off when set to off, false, 0 or
// disabled. Unset or any other value leaves it on.
const liveTVEnvVar = "SILO_JELLYCOMPAT_LIVETV"

// liveTVEnvOff lists the values of liveTVEnvVar that turn the bridge off.
var liveTVEnvOff = []string{"off", "false", "0", "disabled"} //nolint:goconst // accepted spellings, listed verbatim.

// LiveTVEnabledFromEnv reports whether the Live TV bridge should be built.
// getenv is os.Getenv in production.
func LiveTVEnabledFromEnv(getenv func(string) string) bool {
	return !slices.Contains(liveTVEnvOff, strings.ToLower(strings.TrimSpace(getenv(liveTVEnvVar))))
}
