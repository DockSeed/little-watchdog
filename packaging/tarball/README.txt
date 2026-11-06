little-watchdog: install without a package manager

  sudo ./install.sh

This installs the program to /usr/local/bin, creates the user and the config
directory, installs and starts the systemd service, and asks to add you to the
group little-wd (answer y). Then log out and in again and run:

  little-wd

Needs systemd. See first what it would do: ./install.sh --dry-run
Remove: sudo ./install.sh --uninstall (add --purge to delete the config too).

Setup (Telegram bot, checks, dead-man's-switch):
  https://github.com/DockSeed/little-watchdog/blob/main/docs/setup.md
Documentation: https://github.com/DockSeed/little-watchdog, and  man little-wd
Licence: MIT (LICENSE); the Go runtime inside little-wd: BSD-3-Clause (LICENSE.go).
