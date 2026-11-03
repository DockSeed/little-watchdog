little-watchdog: install without a package manager

  sudo ./install.sh

This installs the program to /usr/local/bin, creates the user and the config
directory, installs and starts the systemd service, and adds you to the group
little-wd. Then log out and in again and run:

  little-wd

Needs systemd. Remove with: sudo ./install.sh --uninstall   (add --purge to
delete the config as well). Look first at what it would do: ./install.sh --dry-run

Documentation: https://github.com/DockSeed/little-watchdog
