# Native Linux packages (deb / rpm)

For Docker-free installs on **Debian / Ubuntu** (amd64, arm64) and **RHEL / Fedora / Rocky / AlmaLinux / openSUSE** (x86_64, aarch64). Other architectures (armv7 included): use the [bare-metal deployment](deployment.md) or [Docker](install-docker.md).

## Install

Download the package for your architecture (the filename carries it) from [Releases](https://github.com/Mi-Bee-Studio/MiBeeNvr/releases/latest) and install locally:

```bash
# Debian / Ubuntu (use _arm64.deb on arm64 machines)
sudo apt install ./mibee-nvr_<version>-1_amd64.deb

# RHEL / Fedora / Rocky / AlmaLinux (use .aarch64.rpm on arm64 machines)
sudo dnf install ./mibee-nvr-<version>-1.x86_64.rpm
# openSUSE
sudo zypper install ./mibee-nvr-<version>-1.x86_64.rpm
```

The package ships the `/usr/bin/mibee-nvr` binary, the `mibee-nvr.service` systemd unit and `/etc/mibee-nvr/config.example.yaml` (a commented example, reference only). Installation creates the `nvr` system user and registers the service; start it with:

```bash
sudo systemctl enable --now mibee-nvr
```

The first start auto-generates the default config inside the data directory (the storage root defaults to that same directory); the web UI is then at `http://<host>:9090`.

## Data and config locations

| Path | Contents |
|---|---|
| `/var/lib/mibee-nvr/mibee-nvr.yaml` | Config (auto-generated on first start). The service runs with `ProtectSystem=strict` — `/etc` is read-only, so the config lives next to the data |
| `/var/lib/mibee-nvr` | Data directory: database, recordings |
| `/etc/mibee-nvr/config.example.yaml` | Commented example config (conffile — upgrades never overwrite your edits) |

Logs go to journald: `journalctl -u mibee-nvr -f`.

## Upgrade

```bash
sudo apt install ./mibee-nvr_<new-version>-1_amd64.deb    # or dnf / zypper with the new rpm
```

A running service restarts automatically; config and data are untouched.

## Remove

`apt remove` / `dnf remove` stops and removes the program but **never deletes** `/var/lib/mibee-nvr` (recordings and config stay, protecting against accidental loss). Clean up manually once you are sure:

```bash
sudo systemctl stop mibee-nvr && sudo rm -rf /var/lib/mibee-nvr
```

## Relation to other install methods

- The systemd unit is the package variant of the [bare-metal unit](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/deploy/mibee-nvr.service) — only the binary path differs (`/usr/bin` vs `/usr/local/bin`); security hardening, the crash-loop fuse and the memory-tuning comments are identical.
- Packages are built by CI from a single [nfpm spec](https://github.com/Mi-Bee-Studio/MiBeeNvr/blob/main/deploy/packaging/nfpm.yaml); SHA-256 hashes ship in each release's `checksums.txt`.
- There is no apt/yum repository hosting yet — packages install straight from the release page; open an issue if you want repo hosting.
