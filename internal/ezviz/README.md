# EZVIZ / Hik-Connect (cloud P2P)

Streams Hikvision / EZVIZ cameras and NVRs through the cloud using the proprietary
P2P protocol — no LAN access, port-forwarding, or RTSP required. Fills the gap
noted in the main README ("Hikvision — a lot of proprietary streaming
technologies"); the existing `isapi:` source is two-way-audio backchannel only.

**EZVIZ and Hik-Connect are the same Hikvision consumer cloud** (Hik-Connect is the
current name; EZVIZ the older/retail brand). One account and this one source cover
both — register the device in whichever app you have, then use the credentials here.

```yaml
streams:
  camera1: ezviz://ACCOUNT:PASSWORD@api.hik-connect.com/SERIAL?channel=1&subtype=main
```

- `ACCOUNT:PASSWORD` — Hik-Connect / EZVIZ account credentials
- `SERIAL` — device serial
- `channel` — 1-based channel (default `1`)
- `subtype` — `main` (full resolution) or `sub` (low-res substream) (default `main`)

`hikconnect:` and `ezviz:` are interchangeable aliases for the same source — use
whichever matches the app you registered the device in.

## Browser playback

The device streams HEVC (H.265). Browsers play H.264 over WebRTC/MSE but generally
not raw H.265, so for a browser-facing stream transcode to H.264 with an `ffmpeg:`
source — hardware-accelerated where available:

```yaml
streams:
  garage:      ezviz://ACCOUNT:PASSWORD@api.hik-connect.com/SERIAL?channel=4&subtype=main
  garage_h264: ffmpeg:garage#video=h264#audio=copy#hardware=cuda   # drop #hardware=cuda for software
```

Keep an `#audio=` directive: `ffmpeg:` drops audio without one. Which codec
depends on the player:

- **WebRTC** plays the interleaved G.711 (PCMA) track as-is, so `#audio=copy`
  works and avoids re-encoding.
- **MSE** (the default `stream.html` player in most browsers) cannot decode
  PCMA, so a `#audio=copy` stream plays silently there. Transcode to AAC with
  `#audio=aac` for an MSE-audible stream:

  ```yaml
  garage_mse: ffmpeg:garage#video=h264#audio=aac#hardware=cuda
  ```

The `ffmpeg:` source pulls its input over go2rtc's internal RTSP, so the `rtsp:`
module must stay enabled (it is by default).

## How it works

`pkg/ezviz` speaks the cloud P2P protocol end to end:

1. REST login to the Hik-Connect / EZVIZ account, fetch the per-session P2P
   secret and device routing config (credentials-only — no hardcoded keys).
2. `P2P_SETUP` against the cloud, UDP hole-punch to reach the device directly.
3. `PLAY_REQUEST` → SRT handshake → encrypted media.
4. De-frame Hik-RTP, reassemble fragmented NALs, and hand whole H.265 / H.264
   access units to go2rtc via the RAW path; codec parameters are probed from the
   live stream. Interleaved G.711 A-law audio is surfaced on a second track.

## Status

Verified end to end against real hardware (4K NVR): login → P2P → SRT → HEVC,
sustained live preview at full resolution, `main` and `sub` both working, with
interleaved G.711 (A-law / PCMA) audio.

### Transport mix tested

Media flowed over the **direct, hole-punched P2P path** in every verified run:
once the UDP hole-punch completes, SRT media goes device → client over the
punched socket. The client was behind a normal home/office NAT; testing a
symmetric-NAT setup did not force a different path for this device.

`PLAY_REQUEST` is sent on two paths for reliability — directly to the device and,
in parallel, wrapped in a `TRANSFOR_DATA` message relayed through the P2P server.
That relay carries only the *control* request as a belt-and-suspenders; **media
itself never traverses a relay**, and there is no TCP media-relay fallback in
this implementation. So "relayed" here means relayed control, not relayed video.

See `pkg/ezviz/PROTOCOL.md` for the full wire format and the direct-vs-relayed
breakdown.
