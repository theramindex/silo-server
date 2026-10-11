// theramindex fork: tests for the livetv/v1 contract types against the shared fixtures.

package jellycompat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readLiveTVFixture decodes a contract fixture strictly, so a field added to
// the fixtures without a matching struct field fails here.
func readLiveTVFixture(t *testing.T, name string, into any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts", "livetv-v1", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
}

func TestLiveTVContractFixturesDecode(t *testing.T) {
	t.Run("info", func(t *testing.T) {
		var info liveTVInfo
		readLiveTVFixture(t, "info.json", &info)
		if info.ContractVersion != 1 {
			t.Errorf("contractVersion = %d, want 1", info.ContractVersion)
		}
		if info.Name == "" {
			t.Error("name is empty")
		}
		if len(info.StreamHosts) == 0 {
			t.Error("streamHosts is empty")
		}
	})

	t.Run("channels", func(t *testing.T) {
		var channels []liveTVChannel
		readLiveTVFixture(t, "channels.json", &channels)
		if len(channels) == 0 {
			t.Fatal("no channels decoded")
		}
		byID := make(map[string]liveTVChannel, len(channels))
		for _, ch := range channels {
			if ch.ID == "" || ch.Number == "" || ch.Name == "" || ch.GroupID == "" || ch.Group == "" {
				t.Errorf("channel %+v is missing a required field", ch)
			}
			byID[ch.ID] = ch
		}
		if got := byID["xtream:42"]; got.LogoURL != "http://xtream.example/logos/42.png" || got.GroupID != "7" {
			t.Errorf("xtream:42 = %+v", got)
		}
		if got, ok := byID["m3u:sports.one"]; !ok || got.LogoURL != "" {
			t.Errorf("m3u:sports.one = %+v, ok=%v; want empty logoUrl", got, ok)
		}
	})

	for name, wantMode := range map[string]string{"stream_redirect.json": "redirect", "stream_server.json": "server"} {
		t.Run(name, func(t *testing.T) {
			var stream liveTVStream
			readLiveTVFixture(t, name, &stream)
			if stream.URL == "" {
				t.Error("url is empty")
			}
			if stream.Container != "ts" && stream.Container != "hls" {
				t.Errorf("container = %q", stream.Container)
			}
			if stream.Mode != wantMode {
				t.Errorf("mode = %q, want %q", stream.Mode, wantMode)
			}
		})
	}
}

func TestLiveTVContractPaths(t *testing.T) {
	for got, want := range map[string]string{
		liveTVPathInfo:     "/silo-compat/livetv/v1/info",
		liveTVPathChannels: "/silo-compat/livetv/v1/channels",
		liveTVPathStream:   "/silo-compat/livetv/v1/stream",
	} {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
