# Xboard Node

**简体中文** | [English](README.en.md)

Xboard Node 是 [Xboard](https://github.com/cedar2025/Xboard) 的节点后端，内嵌 sing-box 和 Xray-core，可在一台机器上运行单个或多个面板节点。

> 本项目仅供学习和研究，请遵守所在地法律法规及相关服务条款。

## 功能

- 支持节点模式、机器模式和独立模式；单进程可绑定多个面板或节点。
- 内嵌 sing-box 和 Xray-core；协议支持因内核和配置而异，包括 VMess、VLESS、Trojan、Shadowsocks、Hysteria 等，部分内核还支持 TUIC、AnyTLS。
- 支持 WebSocket 推送与 REST 轮询、用户限速、设备限制及在线 IP 跟踪。
- 支持自定义路由、出站和 TLS 证书配置。

## 环境准备

- 原生安装需要 Linux、`amd64`/`x86_64` 或 `arm64`/`aarch64`，以及正在运行的 systemd 或 OpenRC。安装、升级和卸载需要 root 权限。
- 准备面板地址，以及节点模式的节点 ID 和 server token，或机器模式的机器 ID 和 machine token。
- 服务器需要能访问面板及 GitHub Release，并按面板节点配置放通相应端口。
- Alpine/OpenRC 适配已通过单元测试，但尚未完成真实 Alpine 主机的安装和服务生命周期验证；请先在测试环境验证。

> 命令中的地址、ID 和 `TOKEN` 都是占位符。直接在命令行传入 token 可能暴露在 shell 历史或进程参数中；请限制服务器和终端访问，妥善保护配置、凭据和备份，不要将它们提交到版本库。

## 原生安装

### 1. 安装前置工具

确保已安装 Bash、curl 和 CA 证书。Alpine 默认使用 OpenRC，请以 root 登录后运行：

```bash
apk add --no-cache bash curl ca-certificates
```

Alpine 还需要 OpenRC 已实际运行，且可用 `rc-service`、`rc-update` 和 `supervise-daemon`；普通容器通常没有运行 init。下文命令使用 `sudo`，root 用户应去掉 `sudo`。

### 2. 下载安装脚本

```bash
curl -fsSL https://raw.githubusercontent.com/coder6380/Xboard-Node/fix-dev/install.sh -o install.sh
```

建议先检查下载的脚本，再执行。脚本来自 `fix-dev` 分支，但默认下载最新正式 Release 的二进制；两者可能不属于同一版本。使用 Alpine 等新功能时，请确认所选 Release 已包含对应支持，也可用 `--version` 指定已发布版本。

### 3. 选择部署模式

**节点模式：**使用面板 server token 和节点 ID。

```bash
sudo bash install.sh --mode node --panel https://panel.example.com --token TOKEN --node-id 1
```

**机器模式：**使用机器 token 和机器 ID。

```bash
sudo bash install.sh --mode machine --panel https://panel.example.com --token MACHINE_TOKEN --machine-id 1
```

安装器默认使用 `singbox`，可通过 `--kernel xray` 切换；`--version v2.0.1` 可选择指定 Release。健康检查端口默认为 `65530`，使用 `--health-port 0` 可禁用。其他选项请运行：

```bash
bash install.sh help
```

重复安装会合并或替换相应实例配置，请先备份已有配置和凭据。

### 4. 检查运行状态

```bash
xbctl status
```

```bash
xbctl list
```

```bash
xbctl service logs
```

`xbctl service logs` 默认持续输出日志，可按 `Ctrl+C` 退出。健康检查的 `/healthz` 仅表示进程存活，不保证节点就绪或已连接面板。多实例共享服务，`xbctl health` 的端口选择在某些多实例配置下可能与实际监听端口不一致。

## Docker 部署

镜像仅包含 `xboard-node`，不包含 `xbctl`。下面以 Linux 主机网络和节点模式为例：

```bash
docker run -d --name xboard-node --restart=always --network=host \
  -e apiHost=https://panel.example.com -e apiKey=TOKEN -e nodeID=1 \
  ghcr.io/coder6380/xboard-node:latest
```

查看日志：

```bash
docker logs -f xboard-node
```

也可将配置文件挂载到 `/etc/xboard-node/config.yml`，并根据需要为证书、ACME 状态和缓存配置可写的持久化目录。使用主机网络前请确认平台支持且端口不冲突。容器环境变量可能被有权限查看容器信息的用户读取；生产环境应妥善管理凭据。

```bash
docker run -d --name xboard-node --restart=always --network=host \
  -v /path/to/config.yml:/etc/xboard-node/config.yml:ro \
  ghcr.io/coder6380/xboard-node:latest
```

## 配置与多实例

安装器生成 `/etc/xboard-node/config.yml`，并将凭据保存在 `/etc/xboard-node/credentials.env`。配置及备份均可能包含敏感信息；更改前请备份并限制访问权限。配置格式见 [config.yml.example](config.yml.example)：支持传统单节点格式和 `instances` 多实例格式，实例配置可覆盖顶层默认值。环境变量会覆盖文件配置；多实例环境下覆盖可能影响每个实例。

添加节点或机器绑定（注意添加使用 `--panel-url`）：

```bash
sudo xbctl bind add-node --panel-url https://panel.example.com --token TOKEN --node-id 2
```

```bash
sudo xbctl bind add-machine --panel-url https://panel.example.com --token MACHINE_TOKEN --machine-id 2
```

查看实例并按实例 ID 删除，或按面板及节点 ID 删除（后者使用 `--panel`）：

```bash
xbctl list
```

```bash
sudo xbctl bind remove INSTANCE_ID
```

```bash
sudo xbctl bind remove-node --panel https://panel.example.com --node-id 2
```

机器绑定可用 `xbctl bind remove-machine --panel URL --machine-id ID` 删除。绑定变更会写入配置并重启共享服务；若重启失败，已写入的配置不一定自动回滚。删除最后一个绑定会尝试停止服务，但不会卸载程序。

## 服务、日志与健康检查

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

systemd 日志由 journal 管理。OpenRC 日志保存在 `/var/log/xboard-node.log`，不会自动轮转，请自行配置日志轮转。OpenRC 的 `xbctl service logs` 支持 `-f`、`--follow`、`-n 数量`、`--lines 数量`，不支持其他 journalctl 参数。非 root 用户操作服务时需要可用的 `sudo`。

主要文件位置：

| 用途 | 路径 |
| --- | --- |
| 程序与管理工具 | `/usr/local/bin/xboard-node`、`/usr/local/bin/xbctl` |
| 配置、凭据、元数据 | `/etc/xboard-node/config.yml`、`credentials.env`、`install-meta.json` |
| 实例与安装备份 | `/etc/xboard-node/instances/`、`/etc/xboard-node/backups/` |
| systemd 服务 | `/etc/systemd/system/xboard-node.service` |
| OpenRC 服务与日志 | `/etc/init.d/xboard-node`、`/var/log/xboard-node.log` |

## 升级与卸载

升级前备份配置、凭据和数据。使用下载的安装脚本升级，会重新生成服务文件并在重启后检查健康状态：

```bash
sudo bash install.sh upgrade
```

```bash
sudo bash install.sh upgrade --version v2.0.1
```

也可以使用 `xbctl` 升级；该方式更新二进制并重启服务，但不会重新生成服务文件或执行 HTTP 健康检查：

```bash
sudo xbctl upgrade
```

卸载默认保留 `/etc/xboard-node` 中的配置、凭据和备份，并要求确认：

```bash
sudo xbctl uninstall
```

**危险：**`--purge` 会删除整个 `/etc/xboard-node`，包括配置、凭据和其中的备份；`--yes` 跳过确认。请确认备份后再执行：

```bash
sudo xbctl uninstall --purge --yes
```

OpenRC 日志文件不会由卸载命令自动删除；请按需要自行处理。也可通过下载的 `install.sh uninstall` 执行卸载。

## 故障排查

1. 使用 `xbctl status`、`xbctl service status` 和 `xbctl service logs` 检查进程及错误信息。
2. 确认面板地址、ID、token、服务器时间和网络连接正确，节点端口未被占用且防火墙允许访问。
3. 安装失败时检查系统架构、GitHub Release 访问以及所选 Release 是否支持当前功能；OpenRC 主机还需确认 init 和监督器实际运行。
4. 多实例部署先检查 `xbctl list` 和配置文件；不要将共享进程的健康检查视为各节点分别就绪的证明。

更多命令和选项：

```bash
xbctl help
```

## 开发与发布

项目使用 Go，版本要求见 [go.mod](go.mod)。构建及测试：

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

CI 在测试通过后构建并压缩 Linux amd64/arm64 二进制，再复用二进制构建 Docker 镜像。推送 `fix-dev`、向 `dev` 提交 PR 或手动运行仅构建；推送 `dev` 更新开发镜像及预发布；推送 `v*` 标签发布版本镜像和正式 Release，最高版本还更新 `latest`。工作流需要 Release 和 GHCR 写入权限。

## 扩展文档

- [自定义路由](docs-custom-routes.md)
- [自定义出站](docs-custom-outbounds.md)
- [DNS 提供商与 ACME DNS-01](docs-dns-providers.md)

## 许可证

MPL-2.0。
