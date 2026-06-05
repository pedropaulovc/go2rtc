package ezviz

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// errNotImplemented marks the cloud P2P transport boundary. Everything above it
// — codec probe, frame pump, NAL handoff into go2rtc — is wired and compiles.
// Implementing connect()/ReadFrame() makes the source stream.
var errNotImplemented = errors.New("ezviz: cloud P2P transport not yet implemented")

// config is parsed from the source URL:
//
//	ezviz://ACCOUNT:PASSWORD@API_HOST/SERIAL?channel=1&subtype=main
//	hikconnect://ACCOUNT:PASSWORD@api.hik-connect.com/L38239367?channel=1&subtype=sub
type config struct {
	baseURL  string // https://API_HOST
	account  string
	password string
	serial   string // device serial
	channel  int    // 1-based channel
	subtype  string // "main" (busType main stream) | "sub"
}

// Client is the Hik-Connect / EZVIZ cloud P2P transport.
//
// Responsibilities, by method (≈2.5–3k LOC budget):
//
//	connect():
//	  - REST login + per-session P2P secret fetch (the P2PServerKey + salt index
//	    are returned fresh per session and must never be cached/hardcoded), then
//	    device lookup.
//	  - V3 binary control protocol: TLV bodies, custom CRC-8, AES-128-CBC.
//	  - P2P_SETUP (0x0B02) → UDP hole-punch (0x0C00/0x0C01) → PLAY_REQUEST
//	    (0x0C02), over the V3 protocol.
//	  - Crypto: AES-CBC, ChaCha20, ECDH P-256, HMAC-SHA256 (stdlib +
//	    golang.org/x/crypto/chacha20).
//
//	ReadFrame():
//	  - The device's proprietary one-way SRT dialect: data/ACK routing plus a
//	    reorder buffer. Control (0x807f) and video (0x8060/0x8050) use separate
//	    ACK sequence spaces and MUST be routed independently, or the device
//	    flow-control stalls.
//	  - Media extraction: strip Hik-RTP (12B) + sub (13B) headers and reassemble
//	    RFC 7798 fragmentation units (type 49) into Annex-B NAL units. Playback
//	    recordings arrive as MPEG-PS instead and are demuxed separately.
type Client struct {
	cfg        config
	remoteAddr string // device NAT-mapped addr, filled by connect()
}

// Dial parses the source URL and establishes the P2P session.
func Dial(rawURL string) (*Client, error) {
	cfg, err := parseURL(rawURL)
	if err != nil {
		return nil, err
	}

	c := &Client{cfg: cfg}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

func parseURL(rawURL string) (config, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return config{}, fmt.Errorf("ezviz: bad url: %w", err)
	}

	pass, _ := u.User.Password()
	cfg := config{
		baseURL:  "https://" + u.Host,
		account:  u.User.Username(),
		password: pass,
		serial:   strings.Trim(u.Path, "/"),
		channel:  1,
		subtype:  "main",
	}

	q := u.Query()
	if v := q.Get("channel"); v != "" {
		if cfg.channel, err = strconv.Atoi(v); err != nil {
			return config{}, fmt.Errorf("ezviz: bad channel %q: %w", v, err)
		}
	}
	if v := q.Get("subtype"); v != "" {
		cfg.subtype = v
	}

	if cfg.account == "" || cfg.password == "" || cfg.serial == "" {
		return config{}, errors.New("ezviz: url needs account:password@host/serial")
	}
	return cfg, nil
}

// connect performs: login → P2P secret fetch → P2P_SETUP → hole-punch →
// PLAY_REQUEST → SRT handshake. See the Client doc comment.
func (c *Client) connect() error {
	return errNotImplemented
}

// ReadFrame returns the next demuxed access unit, or io.EOF on stream end.
func (c *Client) ReadFrame() (*Frame, error) {
	return nil, errNotImplemented
}

func (c *Client) Close() error { return nil }

func (c *Client) Protocol() string { return "udp" }

func (c *Client) RemoteAddr() string {
	if c.remoteAddr != "" {
		return c.remoteAddr
	}
	return c.cfg.baseURL
}
