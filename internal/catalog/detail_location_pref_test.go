package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/locationpref"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestSortFileVersions_LocationPreference(t *testing.T) {
	ceph := "/mnt/ceph/Movies/A (2020)/A.mkv"
	cloud := "/mnt/cloud/movies/a.2020.2160p.mkv"

	tests := []struct {
		name       string
		configured bool
		versions   []FileVersion
		wantFirst  int
	}{
		{
			name:       "equal quality: ceph beats larger cloud file",
			configured: true,
			versions: []FileVersion{
				{FileID: 1, FilePath: cloud, Resolution: "1080p", FileSize: 9_000},
				{FileID: 2, FilePath: ceph, Resolution: "1080p", FileSize: 1_000},
			},
			wantFirst: 2,
		},
		{
			name:       "HDR beats location",
			configured: true,
			versions: []FileVersion{
				{FileID: 1, FilePath: ceph, Resolution: "1080p"},
				{FileID: 2, FilePath: cloud, Resolution: "1080p", HDR: true},
			},
			wantFirst: 2,
		},
		{
			name:       "resolution beats location",
			configured: true,
			versions: []FileVersion{
				{FileID: 1, FilePath: ceph, Resolution: "1080p"},
				{FileID: 2, FilePath: cloud, Resolution: "2160p"},
			},
			wantFirst: 2,
		},
		{
			name:       "same storage: rank tie falls through to larger file",
			configured: true,
			versions: []FileVersion{
				{FileID: 1, FilePath: "/mnt/ceph/Movies/A (2020)/A.1080p.mkv", Resolution: "1080p", FileSize: 1_000},
				{FileID: 2, FilePath: ceph, Resolution: "1080p", FileSize: 9_000},
			},
			wantFirst: 2,
		},
		{
			name:       "unset: larger file wins as upstream",
			configured: false,
			versions: []FileVersion{
				{FileID: 1, FilePath: cloud, Resolution: "1080p", FileSize: 9_000},
				{FileID: 2, FilePath: ceph, Resolution: "1080p", FileSize: 1_000},
			},
			wantFirst: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.configured {
				t.Cleanup(locationpref.Set(locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}))
			} else {
				t.Cleanup(locationpref.Set(nil))
			}
			sortFileVersions(tt.versions)
			if tt.versions[0].FileID != tt.wantFirst {
				t.Fatalf("first FileID = %d, want %d", tt.versions[0].FileID, tt.wantFirst)
			}
		})
	}
}

// TestBuildPlaybackInfo_DefaultsToPreferredLocation is the fork guard: it goes
// through the code that produces default_file_id, so it fails if an upstream
// refactor moves default selection away from sortFileVersions.
func TestBuildPlaybackInfo_DefaultsToPreferredLocation(t *testing.T) {
	t.Cleanup(locationpref.Set(locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}))

	_, variants, _, _, _, _, _ := (&DetailService{}).buildPlaybackInfo(context.Background(), []*models.MediaFile{
		{ID: 1, ContentID: "movie-1", FilePath: "/mnt/cloud/movies/a.2020.mkv", Resolution: "1080p", FileSize: 9_000},
		{ID: 2, ContentID: "movie-1", FilePath: "/mnt/ceph/Movies/A (2020)/A.mkv", Resolution: "1080p", FileSize: 1_000},
	}, AccessFilter{}, "movie-1")

	if len(variants) != 1 {
		t.Fatalf("len(variants) = %d, want 1", len(variants))
	}
	if variants[0].DefaultFileID != 2 {
		t.Fatalf("DefaultFileID = %d, want the ceph copy (2)", variants[0].DefaultFileID)
	}
}
