# Xboard Node

[简体中文](README.md) | **English**

Xboard Node is a node backend for [Xboard](https://github.com/cedar2025/Xboard), embedding sing-box and Xray-core. It can run one or multiple panel nodes on a single machine.

> This project is for education and research only. Comply with applicable laws and service terms.

## Features

- Node, machine and standalone modes; one process can bind multiple panels or nodes.
- Embedded sing-box and Xray-core. Protocol availability depends on the kernel and configuration and includes VMess, VLESS, Trojan, Shadowsocks and Hysteria; some kernels also support TUIC and AnyTLS.
- WebSocket push and REST polling, user speed limits, device limits and online IP tracking.
- Custom routes, outbounds and TLS certificate configuration.

## Prerequisites

- Native installation requires Linux on `amd64`/`x86_64` or `arm64`/`aarch64`, with systemd or OpenRC actually running. Installation, upgrades and removal require root.
- Prepare the panel URL and either the node ID and server token for node mode, or the machine ID and machine token for machine mode.
- The server must be able to reach the panel and GitHub Releases. Open the ports required by the panel's node configuration.
- Alpine/OpenRC support has passed unit tests, but installation and the service lifecycle have not yet been validated on a real Alpine host; test in a non-production environment first.

> Addresses, IDs and `TOKEN` values in these examples are placeholders. Tokens passed on the command line can be exposed in shell history or process arguments. Restrict access to the server and terminal, protect configuration, credentials and backups, and never commit them to a repository.

## Native installation

### 1. Install prerequisites

Install Bash, curl and CA certificates. Alpine normally uses OpenRC; log in as root and run:

```bash
apk add --no-cache bash curl ca-certificates
```

OpenRC must actually be running, with `rc-service`, `rc-update` and `supervise-daemon` available. Ordinary containers often do not run an init system. Examples below use `sudo`; root users should omit it.

### 2. Download the installer

```bash
curl -fsSL https://raw.githubusercontent.com/coder6380/Xboard-Node/fix-dev/install.sh -o install.sh
```

Inspect the downloaded script before running it. The script comes from the `fix-dev` branch, but downloads binaries from the latest stable Release by default; they may not be the same version. For newer features such as Alpine support, verify the chosen Release includes them, or select a published version using `--version`.

### 3. Choose a deployment mode

**Node mode:** use the panel server token and node ID.

```bash
sudo bash install.sh --mode node --panel https://panel.example.com --token TOKEN --node-id 1
```

**Machine mode:** use the machine token and machine ID.

```bash
sudo bash install.sh --mode machine --panel https://panel.example.com --token MACHINE_TOKEN --machine-id 1
```

The default kernel is `singbox`; use `--kernel xray` to select Xray. Use `--version v2.0.1` to select a published Release. The health-check port defaults to `65530`; use `--health-port 0` to disable it. For other options:

```bash
bash install.sh help
```

Re-running installation merges or replaces the relevant instance configuration. Back up existing configuration and credentials first.

### 4. Check the service

```bash
xbctl status
```

```bash
xbctl list
```

```bash
xbctl service logs
```

`xbctl service logs` follows the logs by default; press `Ctrl+C` to exit. The `/healthz` endpoint indicates process liveness only, not node readiness or panel connectivity. Instances share a service, and `xbctl health` may choose a different port from the actual listener in some multi-instance configurations.

## Docker deployment

The image contains `xboard-node`, not `xbctl`. This example uses Linux host networking and node mode:

```bash
docker run -d --name xboard-node --restart=always --network=host \
  -e apiHost=https://panel.example.com -e apiKey=TOKEN -e nodeID=1 \
  ghcr.io/coder6380/xboard-node:latest
```

View logs:

```bash
docker logs -f xboard-node
```

Alternatively, mount a configuration file at `/etc/xboard-node/config.yml` and arrange writable persistent storage for certificates, ACME state and caches as needed. Confirm host networking is supported on your platform and ports do not conflict. Users permitted to inspect containers may read container environment variables; manage credentials appropriately in production.

```bash
docker run -d --name xboard-node --restart=always --network=host \
  -v /path/to/config.yml:/etc/xboard-node/config.yml:ro \
  ghcr.io/coder6380/xboard-node:latest
```

## Configuration and multiple instances

The installer creates `/etc/xboard-node/config.yml` and stores credentials in `/etc/xboard-node/credentials.env`. Configuration and backups may contain secrets; back them up and restrict access before making changes. See [config.yml.example](config.yml.example): both legacy single-node and `instances` formats are supported, and instance settings can override top-level defaults. Environment variables override file configuration and may affect every instance in a multi-instance deployment.

Add a node or machine binding (add commands use `--panel-url`):

```bash
sudo xbctl bind add-node --panel-url https://panel.example.com --token TOKEN --node-id 2
```

```bash
sudo xbctl bind add-machine --panel-url https://panel.example.com --token MACHINE_TOKEN --machine-id 2
```

List instances and remove by instance ID, or remove by panel and node ID (the latter uses `--panel`):

```bash
xbctl list
```

```bash
sudo xbctl bind remove INSTANCE_ID
```

```bash
sudo xbctl bind remove-node --panel https://panel.example.com --node-id 2
```

Remove a machine binding with `xbctl bind remove-machine --panel URL --machine-id ID`. Binding changes write configuration and restart the shared service; written configuration is not necessarily rolled back if restart fails. Removing the last binding attempts to stop the service but does not uninstall the program.

## Service, logs and health

```bash
xbctl service status
```

```bash
sudo xbctl service restart
```

```bash
sudo xbctl service stop
```

```bash
sudo xbctl service start
```

```bash
sudo xbctl service enable
```

```bash
sudo xbctl service disable
```

```bash
xbctl health
```

On systemd, logs are managed by the journal. On OpenRC, logs are stored in `/var/log/xboard-node.log` and are not rotated automatically; configure rotation yourself. OpenRC `xbctl service logs` accepts `-f`, `--follow`, `-n COUNT` and `--lines COUNT`, but not other journalctl arguments. Non-root service operations require an available `sudo`.

Key paths:

| Purpose | Path |
| --- | --- |
| Program and CLI | `/usr/local/bin/xboard-node`, `/usr/local/bin/xbctl` |
| Configuration, credentials, metadata | `/etc/xboard-node/config.yml`, `credentials.env`, `install-meta.json` |
| Instances and installer backups | `/etc/xboard-node/instances/`, `/etc/xboard-node/backups/` |
| systemd service | `/etc/systemd/system/xboard-node.service` |
| OpenRC service and log | `/etc/init.d/xboard-node`, `/var/log/xboard-node.log` |

## Upgrade and uninstall

Back up configuration, credentials and data before upgrading. The downloaded installer regenerates the service file and checks health after restarting:

```bash
sudo bash install.sh upgrade
```

```bash
sudo bash install.sh upgrade --version v2.0.1
```

Alternatively, `xbctl` upgrades the binaries and restarts the service, but does not regenerate the service file or perform an HTTP health check:

```bash
sudo xbctl upgrade
```

Uninstallation keeps configuration, credentials and backups under `/etc/xboard-node` by default and asks for confirmation:

```bash
sudo xbctl uninstall
```

**Danger:** `--purge` deletes all of `/etc/xboard-node`, including its configuration, credentials and backups; `--yes` skips confirmation. Verify your backups first:

```bash
sudo xbctl uninstall --purge --yes
```

The uninstall command does not remove the OpenRC log file automatically; handle it separately if needed. You can also uninstall using the downloaded `install.sh uninstall` command.

## Troubleshooting

1. Check the process and errors with `xbctl status`, `xbctl service status` and `xbctl service logs`.
2. Verify the panel URL, ID, token, server time and network connectivity. Ensure node ports are free and allowed through the firewall.
3. For installation failures, check the architecture, access to GitHub Releases and whether the selected Release supports the feature. On OpenRC, verify init and the supervisor are actually running.
4. For multiple instances, inspect `xbctl list` and the configuration. A shared process health check does not prove that each node is ready.

For all commands and options:

```bash
xbctl help
```

## Development and releases

The project uses Go; see [go.mod](go.mod) for the required version. Build and test:

```bash
make build
```

```bash
make build-linux
```

```bash
make build-linux-arm64
```

```bash
make test
```

After tests pass, CI builds and compresses Linux amd64/arm64 binaries, then reuses them to build the Docker image. Pushes to `fix-dev`, PRs targeting `dev`, and manual runs only build. A `dev` push updates the development image and prerelease; pushing a `v*` tag publishes a versioned image and stable Release, and the highest version also updates `latest`. The workflow needs write access to Releases and GHCR.

## Extension guides

- [Custom routes](docs-custom-routes.md)
- [Custom outbounds](docs-custom-outbounds.md)
- [DNS providers and ACME DNS-01](docs-dns-providers.md)

## License

MPL-2.0.
