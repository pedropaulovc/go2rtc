package ezviz

import (
	"fmt"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/h264"
	"github.com/AlexxIT/go2rtc/pkg/h264/annexb"
	"github.com/AlexxIT/go2rtc/pkg/h265"
	"github.com/pion/rtp"
)

// Producer streams a single channel from a Hik-Connect / EZVIZ device over the
// cloud P2P transport, emitting whole H.265/H.264 access units into go2rtc.
type Producer struct {
	core.Connection
	client *Client
}

func NewProducer(rawURL string) (*Producer, error) {
	client, err := Dial(rawURL)
	if err != nil {
		return nil, err
	}

	medias, err := probe(client)
	if err != nil {
		_ = client.Close()
		return nil, err
	}

	return &Producer{
		Connection: core.Connection{
			ID:         core.NewID(),
			FormatName: "ezviz",
			Protocol:   client.Protocol(),
			RemoteAddr: client.RemoteAddr(),
			Source:     rawURL,
			Medias:     medias,
			Transport:  client,
		},
		client: client,
	}, nil
}

// probe reads frames until the video codec can be built from in-band
// parameter sets (VPS/SPS for HEVC, SPS for H264).
func probe(client *Client) ([]*core.Media, error) {
	var vcodec *core.Codec

	for vcodec == nil {
		f, err := client.ReadFrame()
		if err != nil {
			return nil, fmt.Errorf("ezviz: probe: %w", err)
		}
		if f == nil || len(f.Payload) < 5 {
			continue
		}

		buf := annexb.EncodeToAVCC(f.Payload)
		if len(buf) < 5 {
			continue
		}

		switch f.Codec {
		case CodecH265:
			if h265.NALUType(buf) == h265.NALUTypeVPS {
				vcodec = h265.AVCCToCodec(buf)
			}
		case CodecH264:
			if h264.NALUType(buf) == h264.NALUTypeSPS {
				vcodec = h264.AVCCToCodec(buf)
			}
		}
	}

	return []*core.Media{{
		Kind:      core.KindVideo,
		Direction: core.DirectionRecvonly,
		Codecs:    []*core.Codec{vcodec},
	}}, nil
}

func (p *Producer) Start() error {
	for {
		f, err := p.client.ReadFrame()
		if err != nil {
			return err
		}
		if f == nil {
			continue
		}

		var name string
		switch f.Codec {
		case CodecH265:
			name = core.CodecH265
		case CodecH264:
			name = core.CodecH264
		default:
			continue
		}

		pkt := &core.Packet{
			Header:  rtp.Header{Timestamp: f.Timestamp, SequenceNumber: uint16(f.FrameNo)},
			Payload: annexb.EncodeToAVCC(f.Payload),
		}

		for _, recv := range p.Receivers {
			if recv.Codec.Name == name {
				recv.WriteRTP(pkt)
				break
			}
		}
	}
}
