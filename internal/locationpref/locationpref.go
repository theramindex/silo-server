// Package locationpref ranks media files by where they are stored, so that
// between copies of equal quality the one on preferred storage is chosen.
//
// This is a theramindex fork addition. The list is set once at startup from
// SILO_PREFERRED_PATH_PREFIXES; it is package-level rather than threaded
// through constructors so the fork touches as few upstream signatures as
// possible.
package locationpref

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
)

// Prefixes is an ordered list of cleaned absolute path prefixes. Earlier
// entries rank higher.
type Prefixes []string

var current atomic.Pointer[Prefixes]

// Parse splits a comma-separated list into cleaned prefixes. Blank entries are
// dropped silently; relative paths are dropped with a warning; a prefix under
// an earlier one is kept but warned about, because it can never match.
func Parse(raw string) (Prefixes, []string) {
	var out Prefixes
	var warnings []string
	for _, part := range strings.Split(raw, ",") {
		s := strings.TrimSpace(part)
		if s == "" {
			continue
		}
		if !filepath.IsAbs(s) {
			warnings = append(warnings, fmt.Sprintf("ignoring %q: not an absolute path", s))
			continue
		}
		cleaned := filepath.Clean(s)
		if i := matchIndex(out, cleaned); i >= 0 {
			warnings = append(warnings, fmt.Sprintf("%q is under %q, which is listed earlier, so it never matches", cleaned, out[i]))
		}
		out = append(out, cleaned)
	}
	return out, warnings
}

// Set installs p as the server-wide list and returns a func that reinstates
// the previous one. It stores a copy, so the caller may reuse its slice. The
// server calls it once at startup; tests pass the returned func to t.Cleanup.
func Set(p Prefixes) (restore func()) {
	p = slices.Clone(p)
	prev := current.Swap(&p)
	return func() { current.Store(prev) }
}

// Configured reports whether any prefixes are set.
func Configured() bool {
	p := current.Load()
	return p != nil && len(*p) > 0
}

// Rank returns the index of the first prefix that contains path, or the number
// of prefixes when none does. It returns 0 for every path when nothing is
// configured, so callers can compare ranks unconditionally.
func Rank(path string) int {
	p := current.Load()
	if p == nil || len(*p) == 0 {
		return 0
	}
	if i := matchIndex(*p, filepath.Clean(path)); i >= 0 {
		return i
	}
	return len(*p)
}

func matchIndex(prefixes Prefixes, cleaned string) int {
	for i, prefix := range prefixes {
		if cleaned == prefix || strings.HasPrefix(cleaned, strings.TrimSuffix(prefix, "/")+"/") {
			return i
		}
	}
	return -1
}
