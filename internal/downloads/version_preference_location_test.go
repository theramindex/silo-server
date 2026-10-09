package downloads

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/locationpref"
	"github.com/Silo-Server/silo-server/internal/models"
)

func TestPreferredByDefault_LocationPreference(t *testing.T) {
	ceph := &models.MediaFile{ID: 2, FilePath: "/mnt/ceph/Movies/A.mkv", Resolution: "1080p"}
	cloud := &models.MediaFile{ID: 1, FilePath: "/mnt/cloud/movies/a.mkv", Resolution: "1080p"}
	cloudHDR := &models.MediaFile{ID: 3, FilePath: "/mnt/cloud/movies/a.hdr.mkv", Resolution: "1080p", HDR: true}

	t.Run("configured: ceph beats lower-ID cloud", func(t *testing.T) {
		t.Cleanup(locationpref.Set(locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}))
		got := versionPreference{}.pick("movie-1", []*models.MediaFile{cloud, ceph})
		if got.ID != ceph.ID {
			t.Fatalf("pick = %d, want ceph %d", got.ID, ceph.ID)
		}
	})
	t.Run("configured: HDR beats location", func(t *testing.T) {
		t.Cleanup(locationpref.Set(locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}))
		got := versionPreference{}.pick("movie-1", []*models.MediaFile{ceph, cloudHDR})
		if got.ID != cloudHDR.ID {
			t.Fatalf("pick = %d, want cloud HDR %d", got.ID, cloudHDR.ID)
		}
	})
	t.Run("configured: resolution beats location", func(t *testing.T) {
		t.Cleanup(locationpref.Set(locationpref.Prefixes{"/mnt/ceph", "/mnt/cloud"}))
		cloud2160 := &models.MediaFile{ID: 4, FilePath: "/mnt/cloud/movies/a.2160p.mkv", Resolution: "2160p"}
		got := versionPreference{}.pick("movie-1", []*models.MediaFile{ceph, cloud2160})
		if got.ID != cloud2160.ID {
			t.Fatalf("pick = %d, want cloud 2160p %d", got.ID, cloud2160.ID)
		}
	})
	t.Run("unset: HDR ignored, lowest ID wins", func(t *testing.T) {
		t.Cleanup(locationpref.Set(nil))
		got := versionPreference{}.pick("movie-1", []*models.MediaFile{cloudHDR, ceph})
		if got.ID != ceph.ID {
			t.Fatalf("pick = %d, want lowest ID SDR %d over higher-ID HDR %d", got.ID, ceph.ID, cloudHDR.ID)
		}
	})
	t.Run("unset: lowest ID as upstream", func(t *testing.T) {
		t.Cleanup(locationpref.Set(nil))
		got := versionPreference{}.pick("movie-1", []*models.MediaFile{ceph, cloud})
		if got.ID != cloud.ID {
			t.Fatalf("pick = %d, want lowest ID %d", got.ID, cloud.ID)
		}
	})
}
