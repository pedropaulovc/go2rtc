package ezviz

// Codec IDs for a demuxed access unit. The Hik-Connect P2P stream carries
// HEVC for both main and sub channels on current firmware; H264 is kept for
// older/other models.
const (
	CodecH265 byte = iota + 1
	CodecH264
	CodecAAC
)

// Frame is one demuxed access unit handed up by the P2P transport (Client).
//
// Payload is Annex-B (start-code prefixed): Hik-RTP/sub headers stripped and
// RFC 7798 fragmentation units (type 49) reassembled into whole NAL units. The
// producer converts it to AVCC before pushing into go2rtc's pipeline.
type Frame struct {
	Codec     byte
	Payload   []byte // Annex-B NAL units
	Timestamp uint32 // 90 kHz PTS
	FrameNo   uint32
}
