# Security

## Reporting

Report vulnerabilities privately with **Report a vulnerability** on the Security
tab, or by mail to dockseed@proton.me. No public issues with details, please.
Only the newest release is fixed. Hobby project: we try to answer within a week.

## What is protected

The bot token and the ping URL. The service runs as its own user in a systemd
sandbox, opens no port, starts no programs, makes no requests except checks,
Telegram and the ping, and never updates itself. Only members of the group
`little-wd` can read the config. Certificates are always verified, redirects
are not followed, and foreign text is cleaned before it is shown.

## What is not

- Group members can read the token. Root can do anything.
- A `localhost` check proves that something listens, not what.
- Anyone in your chat can ask for `/status`.
- Telegram and the ping host see your messages and pings.
- Without a dead-man's-switch nobody notices if the machine dies.

If the token leaks, revoke it with @BotFather.

## Release signature

Releases are built reproducibly. `SHA256SUMS` is signed with
`ssh-keygen -Y sign -n file` after an independent rebuild gave the same hashes.
The key is listed in `allowed_signers`. If it is lost or leaks, we announce a
new key here and on https://dockseed.de.
