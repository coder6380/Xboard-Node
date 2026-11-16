# xboard-node

Node backend for [Xboard](https://github.com/cedar2025/Xboard). Supports `sing-box` / `xray-core` dual kernels.

> **Disclaimer**: This project is for educational and learning purposes only.

## Features

- Protocols: V2Ray family, Trojan, Shadowsocks, Hysteria2, TUIC, AnyTLS
- Sync: WebSocket push + REST polling dual channel
- User controls: speed limit, device limit, alive-IP tracking, hot update
- Deploy modes: node mode, machine mode, standalone mode
- Multi-instance: single process binding multiple panels / nodes

## Install

### Docker

```bash
docker run -d --restart=always --network=host \
  -e apiHost=https://panel.com -e apiKey=TOKEN -e nodeID=1 \
  ghcr.io/coder6380/xboard-node:latest
```

### Installer (Linux systemd)

```bash
# Node mode
curl -fsSL https://raw.githubusercontent.com/coder6380/Xboard-Node/dev/install.sh | \
  sudo bash -s -- --mode node --panel https://panel.example.com --token TOKEN --node-id 1

# Machine mode
curl -fsSL https://raw.githubusercontent.com/coder6380/Xboard-Node/dev/install.sh | \
  sudo bash -s -- --mode machine --panel https://panel.example.com --token TOKEN --machine-id 1
```

## xbctl

Run `xbctl` after installation for help. Common commands:

```bash
xbctl list                          # list all instances
xbctl status                        # running status
xbctl bind add-node --panel URL --token TOKEN --node-id 1
xbctl bind add-machine --panel URL --token TOKEN --machine-id 1
xbctl bind remove-node --panel URL --node-id 1
xbctl service restart
```

## Configuration

Legacy single-panel config is fully compatible. Appending bindings auto-migrates to `instances` format. See `config.yml.example`.

## Extensions

- Custom routes: [docs-custom-routes.md](docs-custom-routes.md)
- Custom outbounds: [docs-custom-outbounds.md](docs-custom-outbounds.md)
- DNS providers (ACME DNS-01): [docs-dns-providers.md](docs-dns-providers.md)

## CI 与发布

推送 `fix-dev`、向 `dev` 提交 PR 或手动运行 CI 只执行测试和构建，不发布。推送 `dev` 会更新 `dev` 镜像和开发预发布；推送 `v*` 标签会发布对应版本镜像及正式 Release，最高版本标签还会更新 `latest` 镜像。开发 Release 的 `dev` 标签指向该次构建的提交。

CI 在测试通过后构建 amd64、arm64 发布二进制，随后构建镜像；符合发布条件时镜像在构建阶段直接推送，最后发布二进制。Release 二进制和镜像内程序使用 UPX 压缩，并启用完整 gRPC 实现。

首次正式发布前，请在仓库 Actions 设置中允许 GitHub Actions 创建和写入 Release，并确保 `GITHUB_TOKEN` 有权限写入关联的 GHCR 包；推送指向目标提交的 `v*` 标签即可触发。受保护的 `dev` 标签还需允许工作流更新，否则开发发布会失败。

## License

MPL-2.0.
