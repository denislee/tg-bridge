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
starts skip this flow.

```bash
BASE=https://localhost:8443
curl -k $BASE/v1/auth/status
# → {"state":"need_phone"}

curl -k -X POST $BASE/v1/auth/phone  -d '{"phone":"+15551234567"}'
curl -k $BASE/v1/auth/status
# → {"state":"need_code"}

curl -k -X POST $BASE/v1/auth/code   -d '{"code":"12345"}'
# If 2FA is enabled:
curl -k -X POST $BASE/v1/auth/password -d '{"password":"..."}'

curl -k $BASE/v1/auth/status
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

## Deploy (systemd)

```bash
sudo useradd --system --home /opt/tg-bridge --shell /usr/sbin/nologin tgbridge
sudo install -d -o tgbridge -g tgbridge /opt/tg-bridge/data
sudo install -o tgbridge -g tgbridge bin/tg-bridge-linux-arm64 /opt/tg-bridge/tg-bridge
sudo install -o tgbridge -g tgbridge config.yaml /opt/tg-bridge/config.yaml
sudo cp systemd/tg-bridge.service /etc/systemd/system/
sudo systemctl enable --now tg-bridge
```

## License

MIT
