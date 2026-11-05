# little-watchdog

Watches web pages, ports, hosts and its own machine, and tells you on Telegram
when something goes down or comes back. A dead-man's-switch tells you when the
watchdog itself stops. One static binary, about 10 MB of RAM (measured with a
handful of checks, 15 MB with 40), no web interface, no open port. Linux with
systemd. For homelabs, not for critical infrastructure.
Unofficial; not affiliated with Telegram or Healthchecks.io.

```text
 little-wd  › Status                                          homelab  1 FAIL  
╭─────────────────────────────────── Status ───────────────────────────────────╮
│    State   Check         Type   Last result                 Checked  Since   │
│  This machine                                                                │
│    [OK]    cpu           cpu    7% (15m avg)                07:19    06:58   │
│    [OK]    memory        mem    61% available               07:19    06:58   │
│    [OK]    disk /        disk   73% free                    07:19    06:58   │
│    [OK]    temperature   temp   46.2 C                      07:19    06:58   │
│  Services                                                                    │
│    [OK]    nextcloud     http   answers, 200 in 41 ms       07:19    06:58   │
│    [FAIL]  photos        http   connection refused, nothi~  07:19    07:09   │
│    [OK]    ssh           tcp    open in 1ms                 07:19    06:58   │
│    [OK]    nas           host   open in 2ms                 07:19    06:58   │
╰──────────────────────────────────────────────────────────────────────────────╯
 cpu: OK since 06:58. No Telegram message yet.
```

On a terminal of 120 columns or more the menu and Status get a side panel:
in the menu every failing or unstable check with since when and why (or one
line that all is OK), in Status the selected check; below it load, PSI, uptime
and the last 24 hours as curves. little-wd's own RAM and disk writes stand in the menu's bottom line.
From 160 columns the screen stays 160 wide and sits in the middle.

## Install

On Debian, Ubuntu or Raspberry Pi OS:

```sh
wget -q --show-progress https://github.com/DockSeed/little-watchdog/releases/download/v0.5.1/little-watchdog_0.5.1_$(dpkg --print-architecture).deb
sudo apt install ./little-watchdog_0.5.1_*.deb
```

Answer `y` when it asks to add you to the group little-wd. Then log out and in
again (close an SSH session completely, a new shell is not enough) and run
`little-wd`. Nothing to build: the package holds the program.
Next: the Telegram bot, see [docs/setup.md](docs/setup.md). It also shows how
to check the download and set up the dead-man's-switch.

## How it works

By default a check runs once a minute, reports after 3 failed rounds and gives
the all-clear after 2 good rounds; change this under Settings in `little-wd`.
Messages that cannot be sent wait and go out later. `/status` in the chat
lists all checks. Tested with up to 1000 checks on a small arm64 board with 10%
of one core and 32 MB RAM.

## Commands

| Command | Does |
|---|---|
| `little-wd` | Full-screen setup and status (needs a terminal). |
| `little-wd status` | Prints the last results as a table. |
| `little-wd check` | Runs all checks once, prints the results, sends nothing. |
| `little-wd run` | The daemon; the systemd service starts it. |
| `little-wd version` | Prints the version. |

All take `-config PATH`; default `/etc/little-watchdog/little-watchdog.env`, or
`$LITTLE_WATCHDOG_CONFIG`.

## Build from source

Not needed for the packages above.

```sh
go test -race ./...
scripts/build.sh 0.5.1      # all packages in ./dist, same bytes every time
```

## Who wrote this

One human and AI coding agents (Claude). The agents wrote the code, the tests
and the documentation; the human made the decisions. More:
[how we built it](docs/how-we-built-it.md), [security](SECURITY.md).

MIT licence, see [LICENSE](LICENSE).
