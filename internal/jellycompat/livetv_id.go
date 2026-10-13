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

// Byte layout of a Live TV program UUID:
//
//	[0]     EncodedIDLiveChannel (0xF1)
//	[1]     'P'
//	[2:6]   installation id, big-endian uint32
//	[6:8]   zero
//	[8:13]  first 5 bytes of sha256(channel id)
//	[13:16] start, in whole minutes since liveProgramEpoch, big-endian uint24
//
// A program is found again by asking its channel's provider for the programs
// starting in that minute, so no id map has to survive a restart. The channel
// prefix is shorter than a channel id's; a prefix shared by two channels of one
// installation makes their programs unresolvable, never wrong.
const liveProgramTag = 'P'

// liveProgramEpoch is 2020-01-01T00:00:00Z. 24 bits of minutes reach 2051.
const liveProgramEpoch = 1577836800

const liveProgramMaxMinutes = 1<<24 - 1

type liveChannelPrefix [5]byte

// liveProgramKey is the decoded form of a Live TV program compat ID.
type liveProgramKey struct {
	InstallationID int
	Channel        liveChannelPrefix
	StartMinute    int64 // unix seconds / 60
}

func liveChannelPrefixOf(channelID string) liveChannelPrefix {
	hash := liveChannelHash(channelID)
	var prefix liveChannelPrefix
	copy(prefix[:], hash[:5])
	return prefix
}

// encodeLiveProgramID returns "" for a start outside the encodable range.
func encodeLiveProgramID(installationID int, channelID string, startUnix int64) string {
	minutes := startUnix/60 - liveProgramEpoch/60
	if startUnix < liveProgramEpoch || minutes > liveProgramMaxMinutes {
		return ""
	}
	var raw [16]byte
	raw[0] = byte(EncodedIDLiveChannel)
	raw[1] = liveProgramTag
	binary.BigEndian.PutUint32(raw[2:6], uint32(installationID))
	prefix := liveChannelPrefixOf(channelID)
	copy(raw[8:13], prefix[:])
	raw[13], raw[14], raw[15] = byte(minutes>>16), byte(minutes>>8), byte(minutes)
	return uuid.UUID(raw).String()
}

// decodeLiveProgramID accepts the dashed or 32-hex form. It checks syntax only.
func decodeLiveProgramID(raw string) (liveProgramKey, bool) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return liveProgramKey{}, false
	}
	if parsed[0] != byte(EncodedIDLiveChannel) || parsed[1] != liveProgramTag || parsed[6] != 0 || parsed[7] != 0 {
		return liveProgramKey{}, false
	}
	key := liveProgramKey{InstallationID: int(binary.BigEndian.Uint32(parsed[2:6]))}
	copy(key.Channel[:], parsed[8:13])
	minutes := int64(parsed[13])<<16 | int64(parsed[14])<<8 | int64(parsed[15])
	key.StartMinute = liveProgramEpoch/60 + minutes
	return key, true
}

func isLiveProgramID(raw string) bool {
	_, ok := decodeLiveProgramID(raw)
	return ok
}
