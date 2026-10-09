package locationpref

import (
	"slices"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		want         Prefixes
		wantWarnings int
	}{
		{name: "unset", raw: "", want: nil},
		{name: "blanks dropped", raw: " , ,", want: nil},
		{name: "ordered and trimmed", raw: " /mnt/ceph , /mnt/cloud ", want: Prefixes{"/mnt/ceph", "/mnt/cloud"}},
		{name: "trailing slash cleaned", raw: "/mnt/ceph/,/mnt/cloud//", want: Prefixes{"/mnt/ceph", "/mnt/cloud"}},
		{name: "relative dropped with warning", raw: "mnt/ceph,/mnt/cloud", want: Prefixes{"/mnt/cloud"}, wantWarnings: 1},
		{name: "nested under earlier warns but is kept", raw: "/mnt,/mnt/ceph", want: Prefixes{"/mnt", "/mnt/ceph"}, wantWarnings: 1},
		{name: "duplicate warns", raw: "/mnt/ceph,/mnt/ceph", want: Prefixes{"/mnt/ceph", "/mnt/ceph"}, wantWarnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := Parse(tt.raw)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Parse(%q) = %q, want %q", tt.raw, got, tt.want)
			}
			if len(warnings) != tt.wantWarnings {
				t.Fatalf("Parse(%q) warnings = %q, want %d", tt.raw, warnings, tt.wantWarnings)
			}
		})
	}
}

func TestRank(t *testing.T) {
	t.Cleanup(Set(Prefixes{"/mnt/ceph", "/mnt/cloud"}))

	tests := []struct {
		path string
		want int
	}{
		{"/mnt/ceph/Movies/A (2020)/A.mkv", 0},
		{"/mnt/ceph", 0},
		{"/mnt/cloud/movies/a.2020.mkv", 1},
		{"/mnt/cephbackup/Movies/A.mkv", 2}, // boundary: not under /mnt/ceph
		{"/mnt/Ceph/Movies/A.mkv", 2},       // case-sensitive
		{"/data/A.mkv", 2},
		{"/mnt/cloud/../ceph/A.mkv", 0}, // cleaned before matching
	}
	for _, tt := range tests {
		if got := Rank(tt.path); got != tt.want {
			t.Errorf("Rank(%q) = %d, want %d", tt.path, got, tt.want)
		}
	}
}

func TestRankUnconfigured(t *testing.T) {
	t.Cleanup(Set(nil))
	if Configured() {
		t.Fatal("Configured() = true with no prefixes")
	}
	if got := Rank("/mnt/ceph/A.mkv"); got != 0 {
		t.Fatalf("Rank = %d, want 0 when unconfigured", got)
	}
}

func TestRankRootPrefix(t *testing.T) {
	t.Cleanup(Set(Prefixes{"/"}))
	if got := Rank("/anything/A.mkv"); got != 0 {
		t.Fatalf("Rank under / = %d, want 0", got)
	}
}

func TestSetRestores(t *testing.T) {
	t.Cleanup(Set(nil))
	restore := Set(Prefixes{"/mnt/ceph"})
	if !Configured() {
		t.Fatal("Configured() = false after Set")
	}
	restore()
	if Configured() {
		t.Fatal("restore did not reinstate the previous value")
	}
}

func TestSetCopiesPrefixes(t *testing.T) {
	p := Prefixes{"/mnt/ceph", "/mnt/cloud"}
	t.Cleanup(Set(p))

	p[0] = "/mnt/other"

	if got := Rank("/mnt/ceph/movie.mkv"); got != 0 {
		t.Fatalf("Rank(ceph) = %d after caller mutated its slice, want 0", got)
	}
	if got := Rank("/mnt/other/movie.mkv"); got != 2 {
		t.Fatalf("Rank(other) = %d after caller mutated its slice, want 2", got)
	}
}
