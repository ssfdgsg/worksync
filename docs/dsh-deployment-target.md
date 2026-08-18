# dsh on Worksync：部署目标与当前实现

- 状态：Target architecture
- 日期：2026-08-18
- 关联设计：[`worksync-v0-design.md`](worksync-v0-design.md)
- 验证记录：[`E2E-REPORT-2026-08-18-DSH-ALY.md`](E2E-REPORT-2026-08-18-DSH-ALY.md)

## 1. 一句话目标

用户登录任意 Worksync 宿主后直接执行 `dsh`，但宿主入口只负责转发参数；容器选择、
TTY、stdin/stdout/stderr、工作目录、环境变量和生命周期全部由 Worksync 控制，入口不得
绕过 Worksync 直接调用 Podman。

```text
ssh host
  → /usr/local/bin/dsh "$@"
  → worksync exec --tty=auto -- dsh "$@"
  → backend（native-podman | podman-machine）
  → project container
```

## 2. 正式目标拓扑

```text
Host
├── worksync CLI
├── dsh shim（只调用 worksync）
├── Project Spec
└── Backend
    ├── Linux: native rootless Podman
    └── macOS: Podman Machine
        └── Linux development container
            ├── Node + dsh launcher                 [environment]
            ├── /workspace                         [tracked volume]
            ├── portable .dsh config/memory/session [encrypted tracked volume]
            ├── profile node_modules + pnpm store   [per-platform cache]
            └── credentials                         [secret/out-of-band]
```

SSH/SFTP 只负责 Worksync Commit 对象的 push/pull。开发服务流量由 Podman 直接发布，
不经过 SSH transport。

## 3. 宿主命令契约

宿主可以安装同名轻量 shim，但它只能调用 Worksync：

```sh
#!/bin/sh
set -eu
cd /path/to/dsh-project
exec worksync exec --tty=auto -- dsh "$@"
```

目标 CLI：

```text
worksync exec [--tty=auto|always|never] -- command...
worksync shell [--tty=auto|always|never] [-- command...]
```

TTY 语义：

- `auto`：stdin 和 stdout 都是终端且 `TERM` 可用时，调用 `podman exec -it`；否则 `-i`。
- `always`：强制分配 TTY；非终端调用失败并给出明确错误。
- `never`：不分配 TTY，适合脚本、CI 和输出捕获。
- TTY 模式下必须将宿主 stdin/stdout/stderr 直接连接到 Podman，不经过内存 buffer。
- resize、信号和退出码必须透传；WorksSync 仍负责选择受管容器和检查运行状态。

因此 `dsh --version`、`dsh web`、`dsh --profile dsh-tui` 都通过同一 Worksync 入口运行。

## 4. 数据与持久化边界

| 数据 | 目标位置 | Commit 策略 | 跨架构策略 |
|---|---|---|---|
| Node、dsh 全局包、系统工具 | 容器 RootFS | environment | 每个平台独立构建 OCI variant |
| workspace | tracked volume | restic | 直接恢复 |
| settings、MCP、profile manifest | encrypted tracked volume | restic | 直接恢复 |
| memory、sessions、storages | encrypted tracked volume（用户可选） | restic | 直接恢复 |
| profile `node_modules` | per-platform cache | 默认不 push | 目标机 bootstrap 重建 |
| pnpm/npm store | cache volume | 不 push | 目标机国内镜像重建 |
| API key、auth token | secret volume/系统 keychain | 不进入 Commit | 带外注入 |

端口变化只替换容器实例。替换前先创建内部 RootFS checkpoint，并重新挂载相同的
workspace/home/volume，因此未显式提交的环境修改不会丢失。

## 5. 同架构与跨架构部署

### 5.1 同架构

```text
source: up → configure → commit → push
target: pull → rollback → up
```

Environment OCI 和文件卷都可直接恢复。

### 5.2 跨架构

当前 arm64 writable layer 不能直接运行在 amd64。正式目标是一个逻辑 Commit 持有多平台
environment variant：

```text
environment:
  linux/arm64: sha256:...
  linux/amd64: sha256:...
```

在 multi-platform environment 实现前，目标机必须：

1. 从同一 Project Spec 拉取目标架构基础镜像。
2. 恢复 portable workspace 与 `.dsh` 数据。
3. 执行声明式 bootstrap，在目标架构安装 dsh 和插件依赖。
4. 创建该平台自己的 environment commit。

不得把“目标机原生重建”描述成“跨架构 OCI 直接恢复成功”。

## 6. dsh web 网络目标

dsh 拒绝监听 `0.0.0.0`，因为它包含远程代码执行能力。目标部署为：

```text
dsh web 127.0.0.1:3001
  ← authenticated reverse proxy 0.0.0.0:3000（容器内）
  ← Worksync/Podman publish 127.0.0.1:13000（默认）
```

默认只能从宿主访问。远程使用通过 SSH tunnel、VPN 或正式 HTTPS reverse proxy；监听
`0.0.0.0` 前必须同时配置 TLS、认证和云安全组，不能裸露 dsh web。

## 7. 国内机器依赖源

Project Spec/bootstrap 必须允许显式配置镜像源，不能把国外 registry 写死：

- 源码：Gitee。
- Ubuntu 包：阿里云 Ubuntu mirror。
- OCI/Docker 镜像：可配置国内镜像代理。
- npm/dsh 插件：npmmirror。
- Go module：goproxy.cn。

源选择属于部署配置，不写进 Commit Descriptor 的逻辑环境身份。

## 8. aly 当前部署

当前 aly 已验证：

- Ubuntu 24.04 amd64。
- native Podman 4.9.3；本轮以 root 用户运行，尚非最终 rootless 目标。
- dsh `0.1.0-rc.6`，内部依赖已解析到 rc7。
- `dsh-tui@0.8.1`、qqbot、tuiv、web 已安装。
- dsh web 发布为 `127.0.0.1:13000 -> 3000/tcp`，认证前 `401`、认证后 `200`。
- 1.6 GiB 内存机器采用 2 GiB swap、Node 384 MiB heap 和低并发 pnpm bootstrap。
- k3s 安装期间临时停止，完成后已恢复 `active`。

当前与目标的差异：

1. 非交互宿主 `dsh` 已通过 `worksync exec`。
2. 交互 TUI 因 Worksync 还没有 TTY API，shim 暂时调用 `podman exec -it`；这是明确的临时兼容层，不是目标设计。
3. arm64 → amd64 采用目标机原生重建，不是 OCI writable layer 直接恢复。
4. `.dsh` secret 当前通过既有 SSH 点对点复制并设为 `0600`；目标是独立 secret provider。

## 9. 验收条件

- 宿主 shim 的所有路径都只调用 Worksync。
- `dsh --version`、脚本调用和真实 SSH TUI 均通过 Worksync。
- 容器 stop/start 和端口重建后 dsh、workspace、home、插件配置保持。
- Web 未认证拒绝、认证通过，默认不监听公网。
- 同架构 pull/rollback 恢复 environment；跨架构明确选择对应 OCI variant 或 bootstrap。
- Git、remote store 和日志中没有 SSH 私钥、API token 或 restic 密码。

## 10. 实施计划

### P0：容器可写层持久化改造

当前只在 `expose`/`unexpose` 路径执行内部 RootFS checkpoint；配置漂移触发的 `up`
重建尚未统一使用该机制。因此“端口变化不丢状态”已经实现，但“所有自动重建都不丢
可写层”尚未完成。

目标状态机：

```text
running container
  → stop/quiesce
  → checkpoint writable layer
  → persist checkpoint metadata
  → create replacement candidate
  → attach original volumes
  → start + verify candidate
  → atomically switch active container record
  → retain previous checkpoint for recovery
  → reachability-based GC
```

实施项：

1. 把 checkpoint 从 `restartForPorts` 私有逻辑提升为 Runtime/Coordinator 的统一能力。
2. 覆盖端口变更、manifest/config drift、image 更新和运行时修复等所有自动重建路径。
3. State DB 记录 checkpoint image、源容器、平台、原因、创建时间和恢复状态。
4. 新容器成功启动并写入 DB 前，不删除唯一可恢复的旧 checkpoint。
5. 创建/启动失败时允许从 checkpoint 重试，不回退到 manifest base image。
6. 用户执行 `worksync commit` 时可复用或提升当前 checkpoint，避免无意义的重复 commit。
7. 内部 checkpoint 不进入用户 Commit/Ref，也不由 `push` 上传；显式 commit 后才成为可同步 environment。
8. GC 只删除未被 active container、checkout、Commit 或 recovery record 引用的镜像。
9. checkpoint descriptor 必须记录 OCI platform；不同 CPU 架构之间不得复用 writable layer。
10. E2E 覆盖端口重建、配置漂移、创建失败、启动失败、进程中断和 GC 后恢复。

验收标准：任何由 Worksync 自动触发的容器替换，都必须保留可写层和挂载卷；失败时至少
保留一个可重建的 checkpoint，不允许静默回到基础镜像。

### P1：Worksync 原生 TTY

1. 实现 `worksync exec/shell --tty=auto|always|never`。
2. TTY 模式直接透传 IO、resize、信号和退出码。
3. 删除 dsh shim 中直调 `podman exec -it` 的临时分支。
4. 真实 SSH PTY 下验证 dsh TUI。

### P2：跨架构 environment

1. Commit Descriptor 支持 `linux/arm64`、`linux/amd64` 等 environment variants。
2. 没有目标平台 variant 时执行声明式 bootstrap，而不是加载不兼容 OCI。
3. portable `.dsh` 数据与 per-platform plugin cache 分离。
