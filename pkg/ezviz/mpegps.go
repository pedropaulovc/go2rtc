package ezviz

// MPEG Program Stream demux for recording playback (busType=2).
//
// Live preview streams raw Hik-RTP-framed NAL units, but the NVR serves
// recordings as an MPEG Program Stream: the H.265 video and G.711 audio
// elementary streams are wrapped in PES packets and split across SRT packets.
// extractPlaybackPayload strips the 12-byte Hik-RTP header from each packet;
// concatenating the fragments reconstructs the PS byte stream that psDemuxer
// parses here into the same Frame stream the live path produces, so the Producer
// is unchanged.
//
// PS structure (ISO/IEC 13818-1), all units prefixed by the 00 00 01 start code:
//
//	0xBA       pack header   — 14 bytes + (low 3 bits of byte 13) stuffing
//	0xBB       system header — 2-byte length prefixed
//	0xBC       program stream map — 2-byte length prefixed
//	0xBE/0xBF  padding/private 2 — 2-byte length prefixed
//	0xC0-0xDF  audio PES      — G.711 A-law for this device
//	0xE0-0xEF  video PES      — H.265 Annex-B
//	0xB9       program end

const (
	psStartCodeLen = 4 // 00 00 01 <stream_id>

	sidPackHeader   = 0xBA
	sidSystemHeader = 0xBB
	sidProgramEnd   = 0xB9
	sidAudioMin     = 0xC0
	sidAudioMax     = 0xDF
	sidVideoMin     = 0xE0
	sidVideoMax     = 0xEF
)

// psDemuxer accumulates PS fragments and emits whole-access-unit Frames. A
// session is either live or playback, so the demuxer owns its own sequence
// counters and never touches the live timestamp state.
type psDemuxer struct {
	buf []byte

	// Video access unit assembly: PES packets share an AU until the next PES
	// carrying a PTS, which opens the following AU.
	auData []byte
	auPTS  uint32
	auOpen bool

	videoSeq uint32
	audioSeq uint32
}

func newPSDemuxer() *psDemuxer { return &psDemuxer{} }

// write appends a PS fragment and returns any frames it completed.
func (d *psDemuxer) write(frag []byte) []*Frame {
	d.buf = append(d.buf, frag...)
	return d.parse()
}

// flush emits the final in-progress access unit. Call once the stream ends; the
// streaming parser otherwise holds the last AU back until the next PTS arrives.
func (d *psDemuxer) flush() []*Frame {
	if !d.auOpen {
		return nil
	}
	out := d.emitAU()
	d.auData = d.auData[:0]
	d.auOpen = false
	return out
}

// parse consumes every complete PS unit at the head of the buffer, retaining the
// trailing partial unit for the next call.
func (d *psDemuxer) parse() []*Frame {
	var out []*Frame
	i := 0
	for {
		// Resync to the next start code; PS bytes between units are never
		// expected, but a corrupt fragment must not wedge the parser.
		if i+psStartCodeLen > len(d.buf) {
			break
		}
		if d.buf[i] != 0x00 || d.buf[i+1] != 0x00 || d.buf[i+2] != 0x01 {
			i++
			continue
		}

		sid := d.buf[i+3]
		switch {
		case sid == sidPackHeader:
			n := packHeaderLen(d.buf[i:])
			if n == 0 {
				return d.commit(i, out) // need more bytes
			}
			i += n

		case sid == sidProgramEnd:
			i += psStartCodeLen

		case sid == sidSystemHeader || sid < sidAudioMin:
			// All other system/private stream ids are 2-byte length prefixed.
			n, ok := lengthPrefixedLen(d.buf[i:])
			if !ok {
				return d.commit(i, out)
			}
			i += n

		case sid >= sidAudioMin && sid <= sidVideoMax:
			pktLen, ok := lengthPrefixedLen(d.buf[i:])
			if !ok {
				return d.commit(i, out)
			}
			pkt := d.buf[i+6 : i+pktLen]
			es, pts, hasPTS := parsePESPayload(pkt)
			if sid >= sidVideoMin {
				out = d.feedVideo(out, es, pts, hasPTS)
			} else if len(es) > 0 {
				out = append(out, &Frame{
					Codec:     CodecPCMA,
					Payload:   cloneBytes(es),
					Timestamp: rescale(pts, 90000, 8000),
					FrameNo:   d.nextAudioSeq(),
				})
			}
			i += pktLen

		default:
			i++ // unknown id, resync
		}
	}
	return d.commit(i, out)
}

// commit drops the consumed prefix and returns the collected frames.
func (d *psDemuxer) commit(consumed int, out []*Frame) []*Frame {
	if consumed > 0 {
		d.buf = append(d.buf[:0], d.buf[consumed:]...)
	}
	return out
}

// feedVideo appends a video PES payload to the current access unit. A PES that
// carries a PTS closes the previous AU and opens a new one.
func (d *psDemuxer) feedVideo(out []*Frame, es []byte, pts uint32, hasPTS bool) []*Frame {
	if hasPTS && d.auOpen {
		out = append(out, d.emitAU()...)
		d.auData = d.auData[:0]
		d.auOpen = false
	}
	if hasPTS {
		d.auPTS = pts
		d.auOpen = true
	}
	d.auData = append(d.auData, es...)
	return out
}

// emitAU splits the assembled Annex-B access unit into NAL-unit Frames that all
// share the AU's PTS, mirroring the live path (one PTS per access unit).
func (d *psDemuxer) emitAU() []*Frame {
	ts := d.auPTS
	var out []*Frame
	for _, nal := range splitAnnexB(d.auData) {
		out = append(out, &Frame{
			Codec:     CodecH265,
			Payload:   nal,
			Timestamp: ts,
			FrameNo:   d.nextVideoSeq(),
		})
	}
	return out
}

func (d *psDemuxer) nextVideoSeq() uint32 { d.videoSeq++; return d.videoSeq }
func (d *psDemuxer) nextAudioSeq() uint32 { d.audioSeq++; return d.audioSeq }

// packHeaderLen returns the full MPEG-2 pack header length (14 + stuffing), or 0
// if more bytes are needed.
func packHeaderLen(b []byte) int {
	if len(b) < 14 {
		return 0
	}
	stuffing := int(b[13] & 0x07)
	total := 14 + stuffing
	if len(b) < total {
		return 0
	}
	return total
}

// lengthPrefixedLen returns the total length (6 + PES_packet_length) of a
// length-prefixed unit, and whether the whole unit is buffered.
func lengthPrefixedLen(b []byte) (int, bool) {
	if len(b) < 6 {
		return 0, false
	}
	total := 6 + int(b[4])<<8 + int(b[5])
	if total <= 6 || len(b) < total {
		// total<=6 means an unbounded (length 0) PES; this device always sets a
		// real length, so treat it as "need more" rather than guess a boundary.
		return 0, false
	}
	return total, true
}

// parsePESPayload splits a PES packet body (everything after the 6-byte
// start-code+length) into its elementary-stream bytes and optional PTS.
func parsePESPayload(b []byte) (es []byte, pts uint32, hasPTS bool) {
	if len(b) < 3 {
		return b, 0, false
	}
	headerLen := int(b[2])
	if 3+headerLen > len(b) {
		return nil, 0, false
	}
	if b[1]&0x80 != 0 && headerLen >= 5 {
		pts = parsePTS(b[3:8])
		hasPTS = true
	}
	return b[3+headerLen:], pts, hasPTS
}

// parsePTS decodes the 33-bit PTS from the 5-byte PES field (truncated to 32
// bits, which is the RTP timestamp width and wraps the same way).
func parsePTS(b []byte) uint32 {
	v := uint64(b[0]&0x0E)<<29 |
		uint64(b[1])<<22 |
		uint64(b[2]&0xFE)<<14 |
		uint64(b[3])<<7 |
		uint64(b[4])>>1
	return uint32(v)
}

// rescale converts a tick count from one clock rate to another.
func rescale(ticks uint32, from, to uint32) uint32 {
	return uint32(uint64(ticks) * uint64(to) / uint64(from))
}

// splitAnnexB splits an Annex-B buffer into individual NAL units, each returned
// with a leading 4-byte start code so the Producer can convert it to AVCC. A
// trailing zero byte that belongs to the next NAL's 4-byte start code is
// harmless and left on the prior NAL.
func splitAnnexB(b []byte) [][]byte {
	var nals [][]byte
	starts := annexBStarts(b)
	for idx, s := range starts {
		end := len(b)
		if idx+1 < len(starts) {
			end = starts[idx+1]
		}
		nal := b[s+3 : end] // skip the 3-byte 00 00 01 start code
		if len(nal) == 0 {
			continue
		}
		out := make([]byte, 0, len(annexBStartCode)+len(nal))
		out = append(out, annexBStartCode...)
		nals = append(nals, append(out, nal...))
	}
	return nals
}

// annexBStarts returns the offsets of every 00 00 01 start code in b.
func annexBStarts(b []byte) []int {
	var out []int
	for i := 0; i+3 <= len(b); i++ {
		if b[i] == 0x00 && b[i+1] == 0x00 && b[i+2] == 0x01 {
			out = append(out, i)
			i += 2
		}
	}
	return out
}
