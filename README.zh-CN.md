# worksync

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**终端里的便携开发环境** —— 像 Git 管理代码一样管理你的开发环境。`worksync`
把一个 YAML 规格变成 rootless Podman 容器,把容器环境和选定数据卷冻结为不可变、
内容寻址的快照提交,并通过 SSH/SFTP 在机器之间搬运这些提交。

```text
up → modify → commit → push → pull → up
```

**[English](README.md)**

## 特性

- **声明式项目规格**(`worksync.yaml`)—— 一个文件描述运行时、容器镜像、端口
  和数据卷;`worksync init` 直接生成。
- **环境提交** —— 用 `podman commit` 冻结容器 RootFS 并导出为 OCI 归档,入库前
  逐一校验 blob 摘要。
- **数据卷快照** —— 选定卷用 restic 快照进加密仓库。
- **远端同步** —— SSH/SFTP 内容寻址存储,ref 文件带 compare-and-swap,上传去重;
  凭据来自你的 SSH agent,worksync 从不保存密钥。
- **无需守护进程** —— 只调用 `podman`、`restic`、`ssh`/`sftp`,宿主机不装任何常驻服务。

## 环境要求

- Go 1.24+ 构建:`go build ./cmd/worksync`
- [Podman](https://podman.io)(rootless);macOS 上用 `podman machine`。
- [restic](https://restic.net) 用于数据卷快照。
- OpenSSH 的 `ssh`/`sftp` 用于远端 push/pull。
- 远端操作需要你的 SSH agent 中已加载密钥。

## 快速开始

```sh
worksync init            # 在当前目录生成 worksync.yaml
worksync up              # 创建并启动开发容器
worksync shell           # 进入交互式 shell
# ... 修改你的工作区 ...
worksync commit -m "wip: add feature"   # 冻结环境 + 数据卷
worksync log             # 查看提交历史
worksync rollback latest # 回滚到上一个提交

# 迁移到另一台机器
worksync remote add origin ssh://user@host/~/worksync-store
worksync push origin     # 上传对象 + ref(去重)
# 在另一台机器上
worksync pull origin     # 下载并应用
worksync up              # 重建容器
```

## 命令一览

| 命令 | 用途 |
| --- | --- |
| `init` | 生成 `worksync.yaml` |
| `up` | 创建/启动容器(幂等;配置漂移时重建) |
| `status` | 项目/容器/卷/端口状态 |
| `shell`、`exec -- cmd` | 在容器内运行 |
| `stop`、`start`、`rm` | 生命周期(删除卷需单独确认) |
| `ports`、`expose`、`unexpose` | 发布端口 |
| `commit -m MSG` | 冻结环境 + 选定卷 |
| `log`、`tag NAME`、`rollback C` | 提交历史与恢复 |
| `diff` | 容器 RootFS 相对镜像的变更 |
| `remote add NAME URL` | 注册 `ssh://` 远端 |
| `push`、`pull`、`fetch` | 与远端传输提交 |
| `doctor` | 环境诊断 |

`expose`/`unexpose` 会自动 checkpoint 当前容器 RootFS 后替换容器实例；workspace、
home 和命名卷保持原路径重新挂载，因此端口变化不会丢失未显式提交的开发环境状态。

全局参数:`--json`(查询命令输出机器可读格式)与 `--debug`。

## 项目规格

```yaml
schemaVersion: 1
name: demo
runtime:
  engine: podman
  backend: auto          # Linux 用 native-podman,macOS 用 podman-machine
  rootless: true
container:
  image: node:24
  workdir: /workspace
  user: dev
  command: ["/opt/worksync/bin/worksync-agent", "idle"]
  environment:
    NODE_ENV: development
ports:
  - name: web
    target: 3000
    published: auto       # 自动分配并记录,重启复用
    listen: 127.0.0.1
    protocol: tcp
volumes:
  workspace:
    source: { type: host, path: . }
    target: /workspace
    policy: tracked
  home:
    target: /home/dev
    policy: persistent
commit:
  environment: true
  volumes: [workspace, home]
snapshot:
  mode: stop             # stop | command | none
  pre:  ["pg_dump > /backup/db.sql"]
  post: []
remote:
  default: origin
```

## 存储布局

- `$XDG_DATA_HOME/worksync/`(或 `~/Library/Application Support/worksync`)
  - `state.db` —— SQLite(WAL)状态机
  - `projects/<id>/` —— 项目派生状态(远端)
  - `commits/<hex>.json` —— 不可变描述符(内容寻址)
  - `oci/<hex>/` —— 环境 OCI 归档 + 校验过的 blobs
  - `restic/` —— 加密的本地卷仓库
  - `locks/`、`staging/` —— 锁文件与原子写入暂存
  - `data/<project>/` —— 托管卷数据(工作区/卷/...)

环境变量覆盖:`WORKSYNC_DATA_DIR`、`WORKSYNC_CONFIG_DIR`。

## 安全

- restic 仓库始终加密,密码存于 `WORKSYNC_RESTIC_PASSWORD` 或 0600 权限的钥匙圈文件。
- 不存任何 SSH 密钥:远端全部走你的 agent。
- 每个变更命令都会记录 journal,重新获取项目锁时恢复过期条目。
- 只调用 `podman`、`restic`、`ssh`/`sftp`,不安装守护进程。

## 开发

- 设计文档:[`docs/worksync-v0-design.md`](docs/worksync-v0-design.md)
- dsh 部署目标:[`docs/dsh-deployment-target.md`](docs/dsh-deployment-target.md)
- `make test`、`make vet`、`make fmt` —— 详见 [CONTRIBUTING.md](CONTRIBUTING.md)

## 状态

v0 里程碑:M0 规格冻结、M1 CLI+状态、M2 生命周期、M4 本地提交、M5 远端
push/pull/fetch 已实现,并有单元与集成(fake binary)测试覆盖;M6 加固进行中。

## License

[MIT](LICENSE)
