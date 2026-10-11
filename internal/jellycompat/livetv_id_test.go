// theramindex fork: tests for restart-safe Live TV channel IDs.

package jellycompat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestLiveChannelIDRoundTrip(t *testing.T) {
	raw := encodeLiveChannelID(17, "xtream:42")
	key, ok := decodeLiveChannelID(raw)
	if !ok || key.InstallationID != 17 || key.Hash != liveChannelHash("xtream:42") {
		t.Fatalf("round trip: ok=%v key=%+v", ok, key)
	}
	if dashed := uuid.MustParse(raw).String(); !isLiveChannelID(dashed) {
		t.Fatalf("dashed form %q not recognised", dashed)
	}
	if undashed := strings.ReplaceAll(raw, "-", ""); !isLiveChannelID(undashed) {
		t.Fatalf("32-hex form %q not recognised", undashed)
	}
}

func TestLiveChannelIDIsStableAndDistinct(t *testing.T) {
	if a, b := encodeLiveChannelID(17, "xtream:42"), encodeLiveChannelID(17, "xtream:42"); a != b {
		t.Fatalf("encoding is not deterministic: %q != %q", a, b)
	}
	if encodeLiveChannelID(17, "xtream:42") == encodeLiveChannelID(18, "xtream:42") {
		t.Fatal("installation id not part of the encoded id")
	}
	if encodeLiveChannelID(17, "xtream:42") == encodeLiveChannelID(17, "xtream:43") {
		t.Fatal("channel id not part of the encoded id")
	}
}

func TestLiveChannelIDDoesNotCollideWithUpstreamIDs(t *testing.T) {
	codec := NewResourceIDCodec()
	for kind := EncodedIDLibrary; kind <= EncodedIDThemeSong; kind++ {
		if kind == EncodedIDLiveChannel {
			t.Fatal("kind value reused")
		}
		for _, v := range []int64{0, 1, 42, 1 << 40} {
			if isLiveChannelID(codec.EncodeIntID(kind, v)) {
				t.Fatalf("kind %d value %d decoded as live channel", kind, v)
			}
		}
	}
	for i := 0; i < 2000; i++ {
		hashed := codec.EncodeStringID(EncodedIDItem, fmt.Sprintf("unmatched/path/%d.mkv", i))
		if isLiveChannelID(hashed) {
			t.Fatalf("hashed item id %q decoded as live channel", hashed)
		}
	}
	if isLiveChannelID(collectionsViewID) {
		t.Fatal("collections view id decoded as live channel")
	}
}

func TestLiveChannelIDRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "nope", "f14c0000000050000000000000000000"} {
		if isLiveChannelID(raw) {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestCompatPathNormalizesLiveTVSegments(t *testing.T) {
	for in, want := range map[string]string{
		"/livetv/channels":   "/LiveTv/Channels",
		"/emby/LIVETV/INFO":  "/LiveTv/Info",
		"/LiveTv/Channels":   "/LiveTv/Channels",
		"/jellyfin/livetv/x": "/LiveTv/x",
	} {
		if got := canonicalizeCompatPath(in); got != want {
			t.Errorf("canonicalizeCompatPath(%q) = %q, want %q", in, got, want)
		}
	}
}
