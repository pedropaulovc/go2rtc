# EZVIZ / Hik-Connect RE working notes

Working log for the reverse-engineering effort to bring `pkg/ezviz` to parity with
the official Hik-Connect Android app. Confirmed findings get folded into
`PROTOCOL.md`; this file is the running investigation log (hypotheses, dead ends,
raw observations).

Started: 2026-06-06.

## Goal / triggering observation

**Both cameras (lobby ch1, garage ch4) record CONTINUOUSLY** (user-confirmed).
This contradicts what my current playback implementation produces:

- Lobby ch1 playback (`start=07:00:00`): streamed continuously past the 30s/5min
  window — OK-ish, but the device ignored `end`/stopTime.
- Garage ch4 playback (`start=07:00:00`, 5min window): delivered only ~92s of
  media, then the device went **silent** (no EOF, no shutdown) until the RTSP
  read-timeout fired.
- Garage ch4 playback at `09:30`, `09:45`, `09:50`: connected (no SRT error) but
  delivered **0 frames**.

=> If recording is continuous, these are bugs/blind spots in how I request the
playback window (start/stop encoding, seek, "continue"/next-file handshake, or a
keepalive the device needs to keep streaming). The official app must do something
my client doesn't. **The app pcap is ground truth.**

## Assets located

| Asset | Path |
|---|---|
| Android emulator | `~/android-sdk/emulator/emulator` (v36.4.10) |
| AVD (Hik-Connect preinstalled) | `~/.android/avd/hiktest.avd` (android-34, x86_64, google_apis) |
| Hik-Connect APK | `/mnt/d/Users/pedro/Downloads/hik-connect-6-1-30-0118.apk` |
| Native libs (extracted) | `/home/pedro/src/hikconnect-web/native/libs/` |
| Key libs | `libezstreamclient.so` (7.5M), `libsrt.so` (1.1M), `libstunClient.so`, `libezLongLink.so`, `libConvergenceEncrypt.so`, `libencryptprotect.so`, `libHCPreview.so`, `libstreamConfig.so`, `libEZAudioSDK.so` |
| RE tools | tshark, dumpcap, mitmproxy, frida, kawaiidra (Ghidra MCP) |

## Method

1. Launch emulator with `-tcpdump /tmp/hik-capture.pcap` (captures the full VM NIC
   incl. UDP P2P + SRT data plane).
2. Log in (same creds as go2rtc), drive live preview + playback on both cameras,
   scrub/seek in playback. Stop capture.
3. Dissect pcap — focus on the playback request/seek/continue flow.
4. Cross-check field semantics in Ghidra (`libezstreamclient.so`, `libsrt.so`).
5. Fix `pkg/ezviz` to parity; live-test against the real device.
6. Hand iVMS-4200 Windows cross-check to the sandbox Claude (separate prompt).

## Log

- 2026-06-06: Recon complete (assets above). Launching emulator with tcpdump.
- 2026-06-06: Logged into official app (com.connect.enduser v6.1.30), drove
  Lobby (ch1) + Garage Outside (ch4) live + playback + multiple timeline seeks.
  **Confirmed visually**: both cameras show a CONTINUOUS blue recording bar across
  the whole timeline (midnight → now). Garage played the 09:00–10:30 window fine —
  exactly the range my client returns 0 frames for. So the blind spot is real.
- 2026-06-06: **First capture FAILED.** `emulator -tcpdump /tmp/hik-capture.pcap`
  produced 465 pkts / 188 KB over 499 s containing ONLY OS background traffic
  (Google QUIC, mDNS, NTP, connectivitycheck) — zero hik-connect DNS/TLS, not even
  the login HTTPS. Root cause hypothesis: API-34 emulator runs a separate
  **virtio-WiFi** netdev (`wlan0`); `-tcpdump` only dumps the primary radio
  interface (`eth0`/10.0.2.15), so all app traffic (which used WiFi) was invisible.
  Fix to try: `svc wifi disable` in guest → force app onto mobile-data (eth0) which
  `-tcpdump` captures. Alternative: in-guest tcpdump on `any`.
- 2026-06-06: **Second capture SUCCEEDED** (`/tmp/hik-capture2.pcap`, 40 MB / 49597
  pkts / 232 s) after `svc wifi disable`. Topology:
  - Media plane: **one** punched UDP socket `10.0.2.15:10257 ↔ 24.35.64.195:17193`
    (device public IP), 13 MB+, stable for the ENTIRE session incl. live + playback
    + every timeline seek. Seeks do NOT reopen the socket → seek is an in-band
    control msg on the existing SRT channel (matches PROTOCOL.md PLAYBACK_SEEK
    0x0C14 over the data plane).
  - P2P signaling: `43.130.155.63:6002/6003` (Tencent-cloud P2P/STUN). Plaintext
    XML `<Request><DevSerial/></Request>` + md5-ish token — device-lookup/punch.
  - API: TLS to `44.214.84.113:8444` (login + device list + playback metadata).
  - A TCP probe to the device LAN IP `192.168.0.101:9010` (1 pkt each way).
  Media is SRT/Convergence-encrypted; without the app's per-session key the
  PLAY_REQUEST/seek bodies aren't directly readable from the pcap → use Ghidra +
  own-client diagnostics instead.
- 2026-06-06: **MAJOR: the "0 frames" blind spot was a STALE-PASSWORD artifact, not
  a protocol bug.** `/tmp/ezviz-discover.json` carried an old 16-char password
  (redacted); the real current password in `../hikconnect-web/.env.local` is 19 chars. With the correct password, garage ch4 playback at the exact 09:30
  window the app had just played delivers **613 video + 742 audio frames in 25 s**,
  smooth ~24 fps, IDR keyframes at frame #1 (654 KB) and #250 (657 KB). PTS span
  29.6 s footage in 25 s wallclock → playback runs slightly FASTER than realtime
  (device sends as fast as SRT flow control allows). So earlier 0-frame windows ⇒
  login was silently failing on the stale creds. Re-validating the ~92s-stall claim
  with a long-window run next.
- 2026-06-06: **~92s-stall claim DISPROVEN.** Garage ch4 07:00–07:10 (10-min window)
  streamed continuously for 130 s wallclock = 2902 video / 3499 audio frames,
  139.9 s footage, steady ~22 fps the whole time, NO stall. Lobby ch1 09:30 also
  clean (457 video / 22.1 s footage). Both reported symptoms (0 frames + 92 s
  stall) were the stale-password login failure. **No protocol blind spot exists;
  the implementation already streams continuous-recording playback correctly.**
- 2026-06-06: **Window-exhaustion behavior characterized.** Requested a 40 s window
  (07:00:00–07:00:40); device delivered 909 video frames = 43.9 s footage (rounds
  up to the next GOP/segment boundary past the requested stop), then went
  **completely silent** — no PS program-end (00 00 01 B9), no SRT 0x8005 shutdown,
  no EOS of any kind. Probe sat flat at 909 frames for ~48 s until we closed it.
  Confirms the PROTOCOL.md note: consumers only recover via the RTSP read-timeout.
- 2026-06-06: **Open-ended (start-only) playback is NOT supported by omitting stop.**
  Temporarily relaxed the F7 guard + sent an empty stopTime TLV: the device accepts
  the PLAY_REQUEST but streams **zero** media (NewProducer's probe hangs). Reverted.
  So a valid stop time is mandatory; "stream to the live edge" must use a different
  mechanism.
- 2026-06-06: **Ghidra (libezstreamclient.so) — the app's continuous mechanism.**
  Two playback stacks exist: (a) protobuf-based **CAS cloud-relay** (StartPlayBackReq
  + CASClient_Playback{Start,Continue,Pause,Resume,Stop,ChangeRateEx}) and (b) the
  **direct-P2P V3** path my client uses. Continuous/seamless playback past a segment
  or window boundary is driven by repeated **PlaybackContinue** control requests:
  `CP2PV3Client::BuildAndSendPlaybackControlRequest` builds a `tag_V3Attribute` V3
  message and does a synchronous `SendRequest` → `CP2PV3RSP` (request/response on the
  V3 control channel, NOT fire-and-forget in the media stream). Opcode family
  0x0C10–0x0C18 (pause/resume/seek/search/continue) as already in PROTOCOL.md. My
  single-shot client never sends Continue → that is exactly why a finite window goes
  silent at the end while the app keeps rolling.

## Conclusions

1. **The reported "playback blind spot" does not exist as a protocol bug.** The
   0-frame and 92 s-stall observations were a stale 16-char password in
   `/tmp/ezviz-discover.json`; the live account password is 19 chars. With correct
   creds, bounded-window playback of continuous recordings works on both cameras,
   no stall, no 0-frames, audio intact.
2. **Genuine feature gaps vs. the official app (not regressions):**
   - No open-ended "stream to the live edge" playback. The app does it via repeated
     PlaybackContinue control requests; my client streams a single bounded window.
     Omitting stop does NOT work (device streams nothing).
   - At window exhaustion the device goes silent with no EOS; the client only ends
     via the downstream read-timeout instead of a clean stream close.
   - No pause/resume/seek/speed control (already documented as out of scope).
3. **Possible follow-ups (feature work, user's call):** (a) graceful window-end —
   detect playback idle after first media and close the stream cleanly instead of
   hanging; (b) open-ended playback via the PlaybackContinue V3 control loop, for
   true app-parity continuous playback.
