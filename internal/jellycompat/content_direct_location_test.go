package jellycompat

import (
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/locationpref"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestItemDetailToUpstreamOrdersByLocationWithinQuality(t *testing.T) {
	hd := []models.VideoTrack{{Width: 1920, Height: 1080}}
	uhd := []models.VideoTrack{{Width: 3840, Height: 2160}}
	ceph := "/mnt/ceph/Movies/A.mkv"
	cloud := "/mnt/cloud/movies/a.mkv"

	tests := []struct {
		name       string
		configured bool
		versions   []catalog.FileVersion
		want       []int
	}{
		{
			name:       "equal width: ceph first",
			configured: true,
			versions: []catalog.FileVersion{
				{FileID: 1, FilePath: cloud, VideoTracks: hd},
				{FileID: 2, FilePath: ceph, VideoTracks: hd},
			},
			want: []int{2, 1},
		},
		{
			name:       "HDR beats location",
			configured: true,
			versions: []catalog.FileVersion{
				{FileID: 1, FilePath: ceph, VideoTracks: hd},
				{FileID: 2, FilePath: cloud, VideoTracks: hd, HDR: true},
			},
			want: []int{2, 1},
		},
		{
			name:       "width beats location",
			configured: true,
			versions: []catalog.FileVersion{
				{FileID: 1, FilePath: ceph, VideoTracks: hd},
				{FileID: 2, FilePath: cloud, VideoTracks: uhd},
			},
			want: []int{2, 1},
		},
		{
			name:       "unset: input order kept within equal width",
			configured: false,
			versions: []catalog.FileVersion{
				{FileID: 1, FilePath: cloud, VideoTracks: hd},
				{FileID: 2, FilePath: ceph, VideoTracks: hd, HDR: true},
			},
			want: []int{1, 2},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.configured {
				t.Cleanup(locationpref.Set(locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}))
			} else {
				t.Cleanup(locationpref.Set(nil))
			}
			detail := itemDetailToUpstream(&catalog.ItemDetail{Versions: tt.versions})
			got := make([]int, 0, len(detail.Versions))
			for _, v := range detail.Versions {
				got = append(got, v.FileID)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("order = %v, want %v", got, tt.want)
			}
		})
	}
}
