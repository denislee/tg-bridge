# tg-bridge

A small Go service that bridges a **Telegram user account** (not a bot) to
resource-constrained devices over HTTPS+WebSocket. Built for the
[LilyGoLib-PlatformIO](https://github.com/dennislee/LilyGoLib-PlatformIO)
firmware but reusable for any embedded client.

- Runs one MTProto session (one Telegram account) via [gotd/td](https://github.com/gotd/td).
- Exposes a narrow JSON API + WebSocket event stream.
- Self-signed TLS; fingerprint is logged so you can pin it in firmware.
- Bearer-token auth per device.
- Single static binary, no cgo — cross-compiles cleanly for `linux/arm64`
  and `linux/armv7` (Raspberry Pi, rock64, etc).

## Status

Early alpha but feature-complete for v1:

- Dialog list + history + text send/receive
- WebSocket event stream with per-device allow-lists
- Photo fetch via opaque HMAC-signed media IDs; smallest thumbnail served
- LRU on-disk media cache enforced on `media.max_bytes`
- Self-signed TLS with SHA-256 fingerprint logged for pinning
- No cgo, single static binary, cross-compiles to `linux/arm64` and `linux/armv7`

Documents / voice / video are summarized in events but not yet fetchable.

## Build

```bash
make tidy         # first time: download deps
make build        # host binary
make build-arm64  # Raspberry Pi 4 / 5 / Zero 2W (64-bit OS)
make build-armv7  # Raspberry Pi 2/3 (32-bit OS)
```

## Configure

1. Create a Telegram app at <https://my.telegram.org/apps> to get `api_id`
   and `api_hash`.
2. `cp config.example.yaml config.yaml` and fill in the IDs + a random
   bearer token: `openssl rand -hex 32`.
3. Start: `./bin/tg-bridge --config config.yaml`.

## First-time login

Do this from a laptop, not the device. The session is persisted; subsequent
starts skip this flow. All auth endpoints require a bearer token (any one
of the tokens in `clients[]` works) and are rate-limited per source IP.

```bash
BASE=https://localhost:8443
TOKEN="your-configured-bearer-token"
H="Authorization: Bearer $TOKEN"

curl -k -H "$H" $BASE/v1/auth/status
# → {"state":"need_phone"}

curl -k -H "$H" -X POST $BASE/v1/auth/phone    -d '{"phone":"+15551234567"}'
curl -k -H "$H" -X POST $BASE/v1/auth/code     -d '{"code":"12345"}'
# If 2FA is enabled:
curl -k -H "$H" -X POST $BASE/v1/auth/password -d '{"password":"..."}'

curl -k -H "$H" $BASE/v1/auth/status
# → {"state":"authorized"}
```

## Runtime API (bearer required)

| Method | Path | Purpose |
|--------|------|---------|
| GET  | `/v1/me`                      | Own user info |
| GET  | `/v1/chats?limit=N`           | Recent dialog list |
| GET  | `/v1/chats/{id}/messages`     | History; `limit`, `before` query params |
| POST | `/v1/chats/{id}/messages`     | Send text: `{"text": "..."}` |
| POST | `/v1/chats/{id}/read`         | Mark read: `{"up_to": 12345}` |
| GET  | `/v1/media/{id}`              | Fetch photo (smallest thumbnail, cached) |
| GET  | `/v1/events`                  | WebSocket event stream |

Chat IDs follow the Bot API convention:
- user → `user_id` (positive)
- basic group → `-chat_id`
- channel/supergroup → `-1000000000000 - channel_id`

## Event stream

```json
{"type":"message","chat_id":123,"msg_id":456,"from":"Alice","text":"hi","ts":1700000000}
```

## TLS pinning on the ESP32

On first launch the bridge logs the cert SHA-256. Bake it into the firmware
and verify on connect instead of accepting any CA.

## Internet-facing deployment

The ESP32 needs the bridge reachable over the public internet when the
device roams off home Wi-Fi. The recommended setup is a small VPS
(Hetzner CX11, Vultr $5, etc — 1 vCPU / 1 GB RAM is plenty).

**Deploy:**

```bash
scp bin/tg-bridge-linux-arm64 root@vps:/opt/tg-bridge/tg-bridge
scp config.yaml                root@vps:/opt/tg-bridge/config.yaml
scp systemd/tg-bridge.service  root@vps:/etc/systemd/system/
ssh root@vps 'systemctl daemon-reload && systemctl enable --now tg-bridge'
```

**Harden before exposing port 8443:**

1. `tls.hosts` in `config.yaml` must include the public hostname **or**
   IP that the ESP32 will dial. Otherwise the cert fingerprint still
   pins, but SNI / SAN mismatches will show up in logs.
2. `clients[].token` is the only thing standing between the open
   internet and your Telegram account. Use at least 32 bytes of entropy
   (`openssl rand -hex 32`) and never reuse the token across devices.
3. Firewall: only `8443/tcp` inbound. Outbound is unrestricted (MTProto
   uses a rotating set of Telegram IPs).
4. Auth endpoints are rate-limited to 10 requests/minute per IP — that
   covers SMS-code brute-force. Telegram itself rate-limits further.
5. On login, the bridge persists `session_dir/session.json` — the session
   token that *is* your account access. Treat it like a password:
   `chmod 600`, back up only encrypted, and if leaked terminate the
   session from an official Telegram client.

**Token rotation:** edit `config.yaml`, `systemctl restart tg-bridge`.
Old tokens stop working immediately.

**Revoking a stolen device:** same — remove the token, restart.

**Logging of source IPs** for every request goes to stderr (captured by
journald on systemd). Useful as a fail2ban input later.

## Deploy (systemd)

One-shot deploy script covers everything: cross-compile, upload, install,
restart, and print the cert fingerprint you need to pin in firmware.

```bash
scripts/deploy.sh pi@rpi.local                     # defaults to arm64
scripts/deploy.sh pi@rpi.local --arch=armv7        # 32-bit Pi OS
scripts/deploy.sh user@vps --arch=amd64            # cheap VPS
```

Re-running is idempotent and performs an in-place update. Passwordless
sudo is required on the remote host.

Manual deploy if you prefer:

```bash
sudo useradd --system --home /opt/tg-bridge --shell /usr/sbin/nologin tgbridge
sudo install -d -o tgbridge -g tgbridge /opt/tg-bridge/data
sudo install -o tgbridge -g tgbridge bin/tg-bridge-linux-arm64 /opt/tg-bridge/tg-bridge
sudo install -o tgbridge -g tgbridge -m 600 config.yaml /opt/tg-bridge/config.yaml
sudo cp systemd/tg-bridge.service /etc/systemd/system/
sudo systemctl enable --now tg-bridge
sudo journalctl -u tg-bridge -f    # watch for the cert SHA-256 fingerprint
```

## License

MIT
