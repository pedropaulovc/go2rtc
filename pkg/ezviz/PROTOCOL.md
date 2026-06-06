# EZVIZ / Hik-Connect cloud P2P wire protocol

Self-contained reference for the protocol `pkg/ezviz` implements. It was
reverse-engineered from Hikvision's `libezstreamclient.so` and the iVMS-4200
desktop client with Ghidra, and validated against live packet captures from a
4K NVR. Opcodes and field tags below are exactly what the code emits and parses.

## V3 control protocol

The control plane ("V3") is a small binary request/response protocol carried in
UDP datagrams. Every message is a 12-byte header followed by an optional body of
TLV attributes (which may be AES-128-CBC encrypted).

### Header (12 bytes, big-endian multi-byte fields)

```
off  size  field
0    1     magic     high nibble 0xE; first byte emitted is 0xE2
1    1     mask      flag bitfield (see below)
2    2     msgType   opcode
4    4     seqNum    sequence number
8    2     reserved  protocol-version constant 0x6234
10   1     headerLen 0x0C (12); larger when an expand header is present
11   1     crc8      CRC-8 over the whole packet with this byte zeroed
12   ..    body      TLV attributes, optionally AES-128-CBC encrypted
```

### Mask byte

```
bit 7  encrypt        body is AES-128-CBC encrypted
bit 6  saltVersion
bit 5..3  saltIndex   3-bit server salt index
bit 2  expandHeader   an expand header follows the 12-byte base header
bit 1  is2BLen        tag 0x07 attributes use a 2-byte length
bit 0  unused
```

### CRC-8

A custom Hikvision bitwise CRC-8 (not a standard polynomial). Computed over the
entire packet with byte 11 zeroed, then written back to byte 11. See `CRC8` in
`v3.go`; the test vectors in `v3_test.go` pin the exact output.

### TLV attributes

```
tag(1) len(1) value(len)                      # normal attributes
tag(0x07) len_be16(2) value(len)              # only when the is2BLen mask bit is set
```

### Encryption

When the encrypt mask bit is set the body is AES-128-CBC with a fixed IV of
`"01234567"` followed by 8 zero bytes. The key is the per-session P2P secret
fetched from the cloud (never hardcoded; the server rotates 8 salt-indexed keys
and returns one per session, identified by `saltIndex`/`saltVersion`).

### Opcodes used by this implementation

| Opcode   | Name           | Role                                                        |
| -------- | -------------- | ----------------------------------------------------------- |
| `0x0B02` | P2P_SETUP      | Register our NAT-mapped address with a P2P server           |
| `0x0B03` | TRANSFOR_CTRL  | Server response carrying the device's stream port           |
| `0x0B04` | TRANSFOR_DATA  | Wrapper used to relay a control message through the server  |
| `0x0C00` | hole-punch req | Sent by the device to open the NAT path to us               |
| `0x0C01` | hole-punch rsp | Our reply (sent 10×) that completes the punch               |
| `0x0C02` | PLAY_REQUEST   | Start the stream (busType/channel/streamType/session)       |
| `0x0C04` | TEARDOWN       | Stop the stream                                             |

## P2P session flow

```
1. P2P_SETUP (0x0B02) to every P2P server registers our NAT-mapped address
   (the server reads the source of our UDP packet, STUN-style — no public IP
   needed).
2. The 0x0B03 response carries the device's stream port; we pre-punch to it.
3. The device sends a hole-punch request (0x0C00); we reply 0x0C01 ten times.
4. PLAY_REQUEST (0x0C02) is sent two ways (see "Transport mix" below).
5. The device opens an SRT connection and streams media; we ACK and reassemble.
```

### Transport mix: direct vs relayed

The media path is always **direct, hole-punched P2P** — once the NAT punch
completes, SRT media flows device → client over the punched UDP socket. There is
no TCP media-relay fallback in this implementation.

PLAY_REQUEST itself is sent on **two paths** for reliability:

- **Path A — direct:** PLAY_REQUEST (0x0C02) straight to the device over the
  punched socket.
- **Path B — relayed control:** the same request wrapped in a TRANSFOR_DATA
  (0x0B04) message addressed to the P2P server, which forwards it to the device.

Path B only relays the *control* message (a belt-and-suspenders so the device
still receives the play request if the first direct datagram is dropped). Media
never traverses the relay.

## Device SRT dialect

The device speaks a proprietary SRT variant: an induction → conclusion handshake,
then media data packets. It multiplexes **two SRT sub-sessions on one socket** —
a control sub-session (type `0x807f` keepalives) and the video sub-session — with
**independent sequence spaces**. ACKs must be routed by inner payload type so the
control sequence space cannot pollute the video flow-control window; mixing them
stalls the device's sender. See `handleSrtDataPacket` / `sendSrtAck` in
`session.go`.

## Media framing (Hik-RTP)

Media data packets carry a Hik-RTP framing layer. `hikrtp.go` strips the
Hik-RTP/sub headers and reassembles RFC 7798 fragmentation units (FU, NAL type
49) into Annex-B H.265 access units. Interleaved G.711 A-law (PCMA) audio is
demuxed onto a second track. Codec parameters are probed from the live stream.

## Live preview vs. recording playback (busType)

PLAY_REQUEST carries a `busType` attribute (`0x76`) selecting the source:

| busType | Source            | `AttrStartTime`/`AttrStopTime` (`0x7a`/`0x7b`) | `AttrSeekRate` (`0x85`) |
| ------- | ----------------- | ---------------------------------------------- | ----------------------- |
| 1       | live preview      | "today so far" placeholder (device ignores)    | —                       |
| 2       | recording playback| the requested window, camera-local wall clock  | speed multiplier        |

The two modes also differ on the wire:

- **Live (busType=1)** streams Hik-RTP-framed H.265 NAL units (see above).
- **Playback (busType=2)** streams the recording as an **MPEG Program Stream**:
  each SRT data packet is the 12-byte Hik-RTP header followed by a raw PS
  fragment (no sub-header, no FU framing). Concatenating the fragments
  reconstructs the PS, which `mpegps.go` demuxes — pack/system/PSM headers,
  then PES packets (`0xE0`–`0xEF` H.265 video, `0xC0`–`0xDF` G.711 audio) — back
  into the same Annex-B + PCMA Frame stream the live path produces, so the
  Producer is unchanged. Video access units are delimited by the PES PTS.
