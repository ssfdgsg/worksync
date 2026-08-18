# Worksync dsh 跨机器 E2E 报告（Podman Machine → aly）

- 日期：2026-08-18
- 源码提交：`23896e7ba780ffa808dac11d687fa4d542c72e9d`
- 本机：macOS arm64 + Podman Machine（AppleHV）
- 目标机：Ubuntu 24.04 amd64 + native Podman
- 目标：迁移完整 `.dsh` 配置和插件，并验证端口重建不会丢失容器 RootFS

## 1. 结论

本轮 dsh 部署和端口 checkpoint E2E 通过。

- 本机 Podman Machine 中成功运行 `@deepseek-ai/dsh@0.1.0-rc.6`。
- `dsh-tui`、`qqbot`、`tuiv`、`web` 四个 profile 均在 Linux 内安装插件依赖。
- dsh web 未认证访问返回 `401`，携带 token 后返回 `200` 和 DeepSeek Harness 页面。
- 本机和 aly 的 `expose`/`unexpose` 均触发容器替换；容器 ID 改变，但 RootFS marker、dsh 主程序和插件卷保持不变。
- aly 上可直接运行 `/usr/local/bin/dsh --version`，输出 `0.1.0-rc.6`。
- aly 的 k3s 在低内存安装期间临时停止，验证完成后已恢复为 `active`。

本轮同时确认：当前 v0 不能直接把 arm64 容器 writable layer 恢复到 amd64。目标机采用相同 manifest 的 amd64 基础镜像原生重建，再从国内 npm 镜像安装目标架构依赖。

## 2. 端口 checkpoint 修复

动态端口映射在 Podman 创建容器时确定，因此端口变化需要替换容器。修复后的顺序为：

```text
stop
  → podman commit（内部 checkpoint，不进入 Worksync Commit/Ref）
  → podman rm
  → 从 checkpoint 创建新容器
  → 重新挂载原 workspace/home/volume
  → start
```

安全失败语义：

- stop 或 checkpoint 失败时不删除旧容器。
- checkpoint 返回空镜像引用时拒绝继续。
- mount-backed workspace、home 和插件卷保持原路径。
- 内部 checkpoint 不由 `worksync push` 上传。

新增单元测试覆盖：

- `stop → commit → rm` 的严格顺序。
- commit 失败时不得调用 `podman rm`。

## 3. 本机 Podman Machine E2E

### 3.1 dsh 与插件

为避免复制 macOS 原生依赖，迁移 `.dsh` 时排除：

- `node_modules`
- `.pnpm-store`
- 诊断日志

在 Podman Machine 的 Linux arm64 容器内通过 npmmirror 安装：

```text
dsh       0.1.0-rc.6
pnpm      10.34.5
dsh-tui   ready
qqbot     ready
tuiv      ready
web       ready
```

pnpm store 放在 VM 内持久 cache 卷，`node_modules` 写入 tracked `.dsh` 卷。这样避免 pnpm 内容寻址 store 在 virtiofs 上并发导入时出现文件缺失。

### 3.2 dsh web 与认证

dsh 出于 RCE 安全考虑拒绝监听 `0.0.0.0`。测试使用：

```text
dsh web 127.0.0.1:3001
  ← container-local TCP proxy 0.0.0.0:3000
  ← Podman publish 127.0.0.1:10240
```

结果：

```text
未认证 HTTP = 401
已认证 HTTP = 200
页面 title   = DeepSeek Harness
```

### 3.3 端口重建

执行 `expose 3100:13100` 后：

- 容器 ID 改变。
- writable-layer marker 保留。
- dsh 和四个 profile 保留。
- `127.0.0.1:13100` 返回 `200`。

执行 `unexpose 3100` 后再次验证状态保留，`13100` 不再监听。

## 4. SSH store 与跨架构结果

本机创建了两个 dsh commit，最终 commit 为：

```text
sha256:5c30ed2ab65e528d757d273b1f9d7001668b22ed57c8358b8b67a6b1f780348c
```

通过 SSH/SFTP 向 aly store 首次全量 push 成功，共上传 33 个对象。restic 密码未进入远端对象 store。

commit descriptor 记录：

```text
architecture = arm64
os           = darwin
```

aly 为 amd64，因此没有假装执行可移植的 OCI rollback。当前 v0 已在正式设计中把“跨 CPU 架构迁移容器 RootFS writable layer”列为非目标。

这里还暴露一个元数据问题：descriptor 的 `os` 使用宿主机 `darwin`，而 OCI 容器实际为 Linux。后续应分别记录 host platform 与 OCI image platform。

## 5. aly 部署

### 5.1 国内依赖源

- Worksync 源码：Gitee，目标提交 `23896e7`。
- Ubuntu 包：阿里云 Ubuntu mirror。
- Node 基础镜像：DaoCloud Docker mirror。
- dsh/npm 插件：npmmirror。

目标机安装：

```text
Podman 4.9.3
restic 0.16.4
native container arch = amd64/x64
```

### 5.2 低内存处理

aly 总内存约 1.6 GiB，k3s-server 常驻约 630 MiB。首次默认并发 npm/pnpm 安装导致 SSH 无法完成 banner 交换。

重启后使用已有 2 GiB swap，并在用户授权下临时停止 k3s；安装参数调整为：

```text
NODE_OPTIONS=--max-old-space-size=384
npm_config_jobs=1
pnpm --network-concurrency 2 --child-concurrency 1
```

四个 profile 串行安装成功，全程 swap 使用量为 0。完成后 k3s 已恢复 `active`。

### 5.3 最终状态

```text
host dsh version     = 0.1.0-rc.6
dsh container        = running
dsh web publish      = 127.0.0.1:13000 -> 3000/tcp
unauthenticated HTTP = 401
authenticated HTTP   = 200
k3s                  = active
swap used            = 0 B
```

aly native Podman 上也重复通过 `expose`/`unexpose` checkpoint 测试。

## 6. 安全检查

- `.pm/` 整体被 Git 忽略；测试 `.dsh`、sessions、token、restic 密码不进入 Git。
- `.credentials.yaml` 和 `.env` 在 aly 上权限为 `0600`。
- 配置和密钥只通过现有 SSH 连接点对点传输。
- 未新增 `dsh@aly` SSH 授权密钥，未复制私钥，未使用 SSH agent forwarding。
- dsh web 仅发布到 `127.0.0.1`，没有裸露公网 RCE 接口。
- k3s 临时停止后已恢复并验证为 `active`。

## 7. 后续项

1. 增加 multi-platform environment：同一逻辑 commit 可保存多个架构的 OCI environment variant。
2. descriptor 分离 host platform 与 OCI image platform。
3. `doctor` 同时报告 backend capability 和当前 Podman 实际 rootless 状态，避免把“支持 rootless”误读为“当前 rootless”。
4. 为 pnpm/npm 安装提供低内存 profile 或 bootstrap hook。
5. 为 dsh web 增加正式的 authenticated reverse-proxy 配置；公网开放前必须配置 TLS、认证与安全组。
