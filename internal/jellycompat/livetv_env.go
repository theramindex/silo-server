// theramindex fork: SILO_JELLYCOMPAT_LIVETV off switch for the Live TV bridge.

package jellycompat

import "strings"

// liveTVEnvVar turns the Live TV bridge off when set to off, false, 0 or
// disabled. Unset or any other value leaves it on.
const liveTVEnvVar = "SILO_JELLYCOMPAT_LIVETV"

// LiveTVEnabledFromEnv reports whether the Live TV bridge should be built.
// getenv is os.Getenv in production.
func LiveTVEnabledFromEnv(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv(liveTVEnvVar))) {
	case "off", "false", "0", "disabled":
		return false
	}
	return true
}
