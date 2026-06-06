package ezviz

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

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
	session    *session
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

// connect performs: login → P2P config + secret fetch → assemble the session
// config → P2P_SETUP → hole-punch → PLAY_REQUEST → SRT handshake. Media then
// flows asynchronously and is drained by ReadFrame.
func (c *Client) connect() error {
	api := newAPIClient(c.cfg.baseURL)
	if err := api.login(c.cfg.account, c.cfg.password); err != nil {
		return err
	}

	p2p, err := api.getP2PConfig(c.cfg.serial)
	if err != nil {
		return err
	}

	secret, err := api.getP2PSecret()
	if err != nil {
		return err
	}

	// The link key (inner PLAY_REQUEST encryption) is the first 32 ASCII chars
	// of the KMS secret.
	linkKey := []byte(p2p.secretKey)
	if len(linkKey) < 32 {
		return fmt.Errorf("ezviz: KMS secret too short: %d chars", len(linkKey))
	}
	linkKey = linkKey[:32]

	// P2P servers come from the per-device config; fall back to the
	// account-level list returned alongside the secret.
	servers := p2p.servers
	if len(servers) == 0 {
		servers = secret.servers
	}
	if len(servers) == 0 {
		return errors.New("ezviz: no P2P servers available")
	}

	// Device NAT-mapped stream endpoint: prefer the WAN IP, fall back to NET IP.
	deviceIP := p2p.wanIP
	if deviceIP == "" {
		deviceIP = p2p.netIP
	}

	cfg := sessionConfig{
		deviceSerial:     c.cfg.serial,
		devicePublicIP:   deviceIP,
		devicePublicPort: p2p.netStreamPort,
		p2pServers:       servers,
		p2pKey:           secret.key,
		p2pLinkKey:       linkKey,
		p2pKeyVersion:    p2p.keyVersion,
		p2pKeySaltIndex:  secret.saltIndex,
		p2pKeySaltVer:    secret.saltVer,
		userID:           extractUserID(api.sessionID),
		clientID:         randomClientID(),
		channelNo:        c.cfg.channel,
		streamType:       streamTypeFor(c.cfg.subtype),
		busType:          1, // live preview
	}

	sess, err := newSession(cfg)
	if err != nil {
		return err
	}
	if err := sess.start(); err != nil {
		_ = sess.close()
		return err
	}

	c.session = sess
	if deviceIP != "" {
		c.remoteAddr = fmt.Sprintf("%s:%d", deviceIP, p2p.netStreamPort)
	}
	return nil
}

// streamTypeFor maps a URL subtype to the device stream type: main=1, sub=2.
func streamTypeFor(subtype string) int {
	if subtype == "sub" {
		return 2
	}
	return 1
}

// ReadFrame returns the next demuxed access unit, or io.EOF on stream end.
func (c *Client) ReadFrame() (*Frame, error) {
	if c.session == nil {
		return nil, errors.New("ezviz: session not started")
	}
	return c.session.readFrame()
}

func (c *Client) Close() error {
	if c.session == nil {
		return nil
	}
	return c.session.close()
}

func (c *Client) Protocol() string { return "udp" }

func (c *Client) RemoteAddr() string {
	if c.remoteAddr != "" {
		return c.remoteAddr
	}
	return c.cfg.baseURL
}
