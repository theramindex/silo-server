package watchtogether

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/locationpref"
)

func TestRoomVersionBetterFollowsPreferredStorage(t *testing.T) {
	cephSmall := catalog.FileVersion{FileID: 1, Resolution: "1080p", FileSize: 100, FilePath: "/mnt/ceph/movie.mkv"}
	cloudLarge := catalog.FileVersion{FileID: 2, Resolution: "1080p", FileSize: 900, FilePath: "/mnt/cloud/movie.mkv"}
	cephSDR := catalog.FileVersion{FileID: 3, Resolution: "1080p", FilePath: "/mnt/ceph/movie-sdr.mkv"}
	cloudHDR := catalog.FileVersion{FileID: 4, Resolution: "1080p", HDR: true, FilePath: "/mnt/cloud/movie-hdr.mkv"}

	tests := []struct {
		name     string
		prefixes locationpref.Prefixes
		a, b     catalog.FileVersion
		wantAB   bool
		wantBA   bool
	}{
		{"configured: preferred storage beats larger size", locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}, cephSmall, cloudLarge, true, false},
		{"configured: HDR beats preferred storage", locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}, cloudHDR, cephSDR, true, false},
		{"unset: larger size wins as upstream", nil, cloudLarge, cephSmall, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Cleanup(locationpref.Set(tt.prefixes))
			if got := roomVersionBetter(tt.a, tt.b); got != tt.wantAB {
				t.Errorf("roomVersionBetter(a, b) = %v, want %v", got, tt.wantAB)
			}
			if got := roomVersionBetter(tt.b, tt.a); got != tt.wantBA {
				t.Errorf("roomVersionBetter(b, a) = %v, want %v", got, tt.wantBA)
			}
		})
	}
}
