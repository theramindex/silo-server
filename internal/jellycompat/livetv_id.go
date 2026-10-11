// theramindex fork: restart-safe compat IDs for Live TV channels.

package jellycompat

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/google/uuid"
)

// liveChannelKey is the decoded form of a Live TV channel compat ID. The hash
// is a truncated digest of the provider channel ID, so a lookup matches it
// against the current channel list instead of keeping a reverse map that a
// restart would lose.
type liveChannelKey struct {
	InstallationID int
	Hash           [8]byte
}

// Byte layout of a live channel UUID:
//
//	[0]    EncodedIDLiveChannel (0xF1)
//	[1]    'L'
//	[2:6]  installation id, big-endian uint32
//	[6:8]  zero
//	[8:16] first 8 bytes of sha256(channel id)
//
// It cannot collide with upstream ids: numeric ids lead with a kind in 1..11,
// packed content ids lead with 2 or 4, and SHA1 name-based UUIDs always carry
// version nibble 5 in byte 6, which the zero check rejects.
const liveChannelTag = 'L'

func liveChannelHash(channelID string) [8]byte {
	sum := sha256.Sum256([]byte(channelID))
	var hash [8]byte
	copy(hash[:], sum[:8])
	return hash
}

func encodeLiveChannelID(installationID int, channelID string) string {
	var raw [16]byte
	raw[0] = byte(EncodedIDLiveChannel)
	raw[1] = liveChannelTag
	binary.BigEndian.PutUint32(raw[2:6], uint32(installationID))
	hash := liveChannelHash(channelID)
	copy(raw[8:], hash[:])
	return uuid.UUID(raw).String()
}

// decodeLiveChannelID accepts the dashed or 32-hex form. It checks syntax only;
// whether the channel exists is the caller's lookup.
func decodeLiveChannelID(raw string) (liveChannelKey, bool) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return liveChannelKey{}, false
	}
	if parsed[0] != byte(EncodedIDLiveChannel) || parsed[1] != liveChannelTag || parsed[6] != 0 || parsed[7] != 0 {
		return liveChannelKey{}, false
	}
	key := liveChannelKey{InstallationID: int(binary.BigEndian.Uint32(parsed[2:6]))}
	copy(key.Hash[:], parsed[8:])
	return key, true
}

func isLiveChannelID(raw string) bool {
	_, ok := decodeLiveChannelID(raw)
	return ok
}
