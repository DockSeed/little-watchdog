# little-watchdog

Watches web pages, ports, hosts and its own machine, and tells you on Telegram
when something goes down or comes back. A dead-man's-switch tells you when the
watchdog itself stops. One static binary, about 12 MB of RAM, no web interface,
no open port. Linux with systemd. For homelabs, not for critical infrastructure.

```text
 little-wd  › Status                    homelab │ 10 OK  1 FAIL  sent 8 min ago
╭─────────────────────────────────── Status ───────────────────────────────────╮
│ Services                                                                     │
│ [OK]   nextcloud     http   HTTP 200 in 41ms                      3 h 0 min  │
│ [FAIL] photos        http   dial tcp 192.0.2.12:2283: connect: conne~ 9 min  │
│ [OK]   ssh           tcp    open in 1ms                           3 h 0 min  │
│ [OK]   nas           host   open in 2ms                           3 h 0 min  │
│ This host                                                                    │
│ [OK]   cpu           cpu    7% (15m avg)                          3 h 0 min  │
│ [OK]   memory        mem    61% available                         3 h 0 min  │
│ [OK]   disk /        disk   73% free                              3 h 0 min  │
│ [OK]   temperature   temp   46.2 C                                3 h 0 min  │
╰──────────────────────────────────────────────────────────────────────────────╯
 photos: failing since 07:19, 3 of 3 tries. Telegram sent at 07:20.
```

## Install

On Debian, Ubuntu or Raspberry Pi OS:

```sh
wget https://github.com/DockSeed/little-watchdog/releases/download/v0.1.0/little-watchdog_0.1.0_$(dpkg --print-architecture).deb
sudo apt install ./little-watchdog_0.1.0_*.deb
```

Then log out and in again and run `little-wd`. How to check the download,
Telegram bot and dead-man's-switch: [docs/setup.md](docs/setup.md).

## How it works

A check reports after 3 failed rounds (one round per minute) and again when it
is back. Messages that cannot be sent wait and go out later. `/status` in the
chat lists all checks. Tested with up to 1000 checks on a small arm64 board
with 10% of one core and 32 MB RAM.

## Build

```sh
go test -race ./...
scripts/build.sh 0.1.0      # all packages in ./dist, same bytes every time
```

## Who wrote this

One human and AI coding agents (Claude). The agents wrote the code, the tests
and the documentation; the human made the decisions. More:
[how we built it](docs/how-we-built-it.md), [security](SECURITY.md).

MIT licence, see [LICENSE](LICENSE).
