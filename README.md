# little-watchdog

Watches web pages, ports, hosts and its own machine, and tells you on Telegram
when something goes down or comes back. A dead-man's-switch tells you when the
watchdog itself stops. One static binary, about 10 MB of RAM (measured with a
handful of checks, 15 MB with 40), no web interface, no open port. Linux with systemd. For homelabs, not for critical infrastructure.

```text
 little-wd  › Status                                          homelab  1 FAIL  
╭─────────────────────────────────── Status ───────────────────────────────────╮
│ This machine                                                                 │
│ [OK]   cpu           cpu    7% (15m avg) · 07:19                       29 s  │
│ [OK]   memory        mem    61% available · 07:19                      29 s  │
│ [OK]   disk /        disk   73% free · 07:19                           29 s  │
│ [OK]   temperature   temp   46.2 C · 07:19                             29 s  │
│ Services                                                                     │
│ [OK]   nextcloud     http   HTTP 200 in 41ms · 07:19                   29 s  │
│ [FAIL] photos        http   connection refused, nothing listens on po~ 29 s  │
│ [OK]   ssh           tcp    open in 1ms · 07:19                        29 s  │
│ [OK]   nas           host   open in 2ms · 07:19                        29 s  │
╰──────────────────────────────────────────────────────────────────────────────╯
 cpu: OK since 07:19. No Telegram message yet.
```

## Install

On Debian, Ubuntu or Raspberry Pi OS:

```sh
wget -q --show-progress https://github.com/DockSeed/little-watchdog/releases/download/v0.1.0/little-watchdog_0.1.0_$(dpkg --print-architecture).deb
sudo apt install ./little-watchdog_0.1.0_*.deb
```

Then log out and in again (close an SSH session completely, a new shell is not
enough) and run `little-wd`. Nothing to build: the package holds the program.
Next: the Telegram bot, see [docs/setup.md](docs/setup.md). It also shows how
to check the download and set up the dead-man's-switch.

## How it works

A check reports after 3 failed rounds (one round per minute) and again when it
is back. Messages that cannot be sent wait and go out later. `/status` in the
chat lists all checks. Tested with up to 1000 checks on a small arm64 board
with 10% of one core and 32 MB RAM.

## Build from source

Not needed for the packages above.

```sh
go test -race ./...
scripts/build.sh 0.1.0      # all packages in ./dist, same bytes every time
```

## Who wrote this

One human and AI coding agents (Claude). The agents wrote the code, the tests
and the documentation; the human made the decisions. More:
[how we built it](docs/how-we-built-it.md), [security](SECURITY.md).

MIT licence, see [LICENSE](LICENSE).
