# Setup

## 1. Install

Download only from the
[releases](https://github.com/DockSeed/little-watchdog/releases). The command
picks the right file for your device (`amd64`, `arm64` or `armhf`):

```sh
v=0.4.0
base=https://github.com/DockSeed/little-watchdog
deb=little-watchdog_${v}_$(dpkg --print-architecture).deb
wget -q --show-progress $base/releases/download/v$v/$deb $base/releases/download/v$v/SHA256SUMS \
  $base/releases/download/v$v/SHA256SUMS.sig $base/raw/v$v/allowed_signers
```

Check it. Both lines must say OK or Good; the key fingerprint must be
`SHA256:CMUY2ymSviJh+qEqswCASwZfhcosZc9kMfzcWAjuHEs`, as on the release page.
This catches a changed download, not a takeover of the whole GitHub account:

```sh
sha256sum -c --ignore-missing SHA256SUMS
ssh-keygen -Y verify -f allowed_signers -I release@little-watchdog -n file -s SHA256SUMS.sig < SHA256SUMS
```

Install, then log out and in again (close an SSH session completely, a new
shell is not enough; with SSH connection sharing also run `ssh -O exit HOST`):

```sh
sudo apt install ./$deb
```

Other distributions: unpack the `.tar.gz` and run `sudo ./install.sh`.

## 2. Telegram bot

1. You need a Telegram account. It is free and needs a phone number. Alarms
   reach you best through the app on your phone
   ([telegram.org/apps](https://telegram.org/apps)); for the steps below,
   [Telegram Web](https://web.telegram.org/a/) next to your terminal is handy.
2. Open a chat with **@BotFather**, send `/newbot`, choose a name. You get a
   **token**. Keep it secret: whoever has it can send as your bot.
3. Open a chat with your new bot and press **Start**.
4. Run `little-wd`, choose **Telegram**, paste the token, and send the shown
   `/start CODE` to your bot. A test message confirms it.

For a group: add the bot to the group and send `/start@your_bot CODE` there.
Use a separate bot for the watchdog. If the token leaks, revoke it with
@BotFather and paste the new one.

## 3. Checks

In `little-wd` choose **Add check** and type an address; the name below it is
optional (any text, shown in Telegram). little-wd works out what to watch:

| Address | Watched as | OK when |
|---|---|---|
| `192.168.1.10` or `nas.lan` | ping | it answers ping |
| `192.168.1.10:2283` or `https://example.org` | web, lenient | any answer below 500, after redirects; a port that answers but is no website counts too |
| `https://example.org/health` (with a path) | web, strict | 200 to 299, after redirects |

Enter saves the address at once, checks it once and shows the result. A
failed check is saved too, and the result says why. The form is then empty for
the next address. Esc leaves, also while the check runs; what was saved stays. Some machines (Windows) do not answer ping:
give a port then, for example `192.168.1.20:3389`.

**Status** sorts the checks of one host under its name (the IP or host name
from the address, without port or path), so `192.168.1.10`, `:2283` and
`:8096` on the same machine stand together. A host with a single check gets
no extra line. Press `a` on a check in Status to add another one on its host:
the address starts with the host, type only the port (`:8096`) and a name.
Each one stays a check of its own.

**Screen sizes.** 80×24 is enough for everything, in one column. From 120
columns a panel on the right shows this machine (load, PSI, uptime, and
the last 24 hours as curves of CPU, RAM, disk, temperature and waiting time) and, in
the menu, every failing or unstable check with since when and why; when all
is fine it says so in one line. In Status it shows the selected check
instead, with how often it answered and how fast over the last 24 hours. From 160 columns nothing new comes: the screen stays 160 wide and
sits in the middle. What little-wd itself costs (RAM, CPU, disk writes) is
the last line of the menu.

The own machine (disk, memory, temperature, CPU) is watched by default; change its
limits under **System checks**. The temperature check takes the hottest sensor;
to pick one, fill in **Sensor** under System checks > temperature. **Settings**
sets how often the checks run and after how many rounds the alarm and the
all-clear go out. `?` shows the keys. Hand editing: [config.md](config.md).

## 4. Dead-man's-switch (recommended)

little-watchdog pings a URL every 2 minutes by default (**Every** under
Dead-man switch in `little-wd`, at least 60s). If the pings stop, because the
machine or the watchdog died, [Healthchecks.io](https://healthchecks.io) alarms
you. It has a free plan and can also be self-hosted.

1. Create an account and a check.
2. Set **Period** to the same time as Every and **Grace Time** to at least
   twice that (4 minutes for 2m).
3. Copy the ping URL (keep it secret) and paste it in `little-wd` under
   **Dead-man switch**. Later, an empty URL keeps the current one and `off`
   switches the dead-man's-switch off.
4. In Healthchecks, set up a notification other than Telegram, for example
   email.

If Telegram stays unusable, little-watchdog pings the URL with `/fail`.

## Several machines

Give each machine its own bot: ask @BotFather for one more bot per machine, add
all of them to one group, and pair each machine with that group (step 2). Every
message starts with the machine name, so you see where it comes from. In the
group, `/status@name_of_the_bot` asks one machine; a plain `/status` is answered
by every machine in the group.

Two machines must not share one bot. Telegram hands a bot's messages to one
reader only, so `/status` would stop working, and `little-wd` then shows
"another program uses this bot".

## Update and remove

Update: install the newer `.deb`. Remove: `sudo apt remove little-watchdog`
(`purge` also deletes the config with the bot token). Tar.gz:
`sudo ./install.sh --uninstall [--purge]`.

If nothing arrives: `journalctl -u little-watchdog -e`. The menu also says when
the service is not running or your login lacks the group.

Sources: [core.telegram.org/bots/tutorial](https://core.telegram.org/bots/tutorial),
[healthchecks.io/docs](https://healthchecks.io/docs/)
