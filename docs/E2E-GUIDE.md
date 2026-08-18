# worksync E2E 操作指南

> 本指南覆盖:前置准备 → 单机生命周期 E2E(up/exec/commit/log/tag/rollback)→ 远端同步 E2E(push/pull/fetch)。
> 环境:macOS + Podman Desktop(podman-machine)+ 真实 restic。

## 0. 前置准备(每条命令都必须带的 env 块)

真实机器环境（沙箱会挡默认配置目录，因此重定向 HOME/XDG）：

```bash
export REPO="$(git rev-parse --show-toplevel)"
export E2E_ROOT="$REPO/.pm"
export HOME="$E2E_ROOT/home"
export XDG_CONFIG_HOME="$E2E_ROOT/config"
export XDG_DATA_HOME="$E2E_ROOT/data"
export CONTAINERS_CONF="$E2E_ROOT/config/containers/containers.conf"
export PATH=/tmp/wb-real-podman/bin:$PATH
# worksync 自身数据目录(测试专用隔离)
export WORKSYNC_DATA_DIR="$E2E_ROOT/wbdata"
export WORKSYNC_CONFIG_DIR="$E2E_ROOT/wbconf"
```

构建二进制:
```bash
export GOMODCACHE="$REPO/.modcache" GOPATH="$REPO/.gopath" GOCACHE="$REPO/.gocache"
cd "$REPO" && go build -o /tmp/worksync ./cmd/worksync
```

## 1. 单机生命周期 E2E

测试项目在 `$E2E_ROOT/proj`（已含 `worksync.yaml` + `HOST.txt`）。

```bash
cd "$E2E_ROOT/proj"

# 1) 创建/启动容器(幂等:已存在且配置未变时直接 start)
/tmp/worksync up
#    期望输出: pulling busybox... / container worksync-realtest is running
#              web -> 127.0.0.1:10241(端口 auto 分配,复跑不变)

# 2) 在容器里执行命令(验证卷挂载 + agent)
/tmp/worksync exec -- cat /workspace/HOST.txt   # 期望: hello from host

# 3) 状态检查
/tmp/worksync status && /tmp/worksync status --json && /tmp/worksync ports

# 4) 提交(真机核心:restic backup --json 快照 ID + podman commit)
/tmp/worksync commit -m "e2e first commit"
#    期望: stopping container / freezing container rootfs /
#          snapshotting workspace... / committed sha256:xxxx

# 5) 日志
/tmp/worksync log && /tmp/worksync log --json

# 6) 打标签
/tmp/worksync tag v1
/tmp/worksync tag v1 <other-commit-sha>

# 7) 回滚演练:改动工作区再回滚,验证 restore 重定位
echo DRIFTED >> HOST.txt
cat HOST.txt
/tmp/worksync rollback v1
#    期望: stopped container / loading environment image /
#          restoring workspace -> .../proj / rolled back
cat HOST.txt   # DRIFTED 消失,数据放回卷根(无嵌套)

# 8) 重新拉起(rollback 后容器已删)
/tmp/worksync up
/tmp/worksync exec -- cat /workspace/HOST.txt

# 9) 停止 / 启动 / 删除
/tmp/worksync stop && /tmp/worksync start
/tmp/worksync rm
/tmp/worksync rm --volumes --yes
```

## 2. 远端同步 E2E(push / pull / fetch)

### 2.0 前置:开启 macOS Remote Login(必做,当前未开)

系统设置 → 通用 → 共享 → 远程登录(SSH)→ 打开。
或命令行:
```bash
sudo systemsetup -setremotelogin on
```
验证:
```bash
ssh -o BatchMode=yes -o ConnectTimeout=3 localhost 'echo ssh-ok'   # 期望 ssh-ok
```

### 2.1 单机自玩(本机当远端)

```bash
cd "$E2E_ROOT/proj"

# 注册远端(路径 ~ 相对远端 HOME)
/tmp/worksync remote add origin ssh://$(whoami)@localhost/~/worksync-store

# 推送(首次全量,二次增量去重)
/tmp/worksync push origin
/tmp/worksync push origin v1

# 拉取(纯对象,不动工作区)
/tmp/worksync fetch origin

# 模拟第二台机器:隔离数据目录 + 项目副本
mkdir -p /tmp/wbclone/proj && cp -r "$E2E_ROOT/proj" /tmp/wbclone/
export WORKSYNC_DATA_DIR=/tmp/wbclone/wbdata WORKSYNC_CONFIG_DIR=/tmp/wbclone/wbconf
cd /tmp/wbclone/proj

# 新机器 pull(下载提交链 + OCI 镜像 + restic 快照)
/tmp/worksync remote add origin ssh://$(whoami)@localhost/~/worksync-store
/tmp/worksync pull origin v1

# 回滚到拉下来的提交(环境镜像 load + 卷 restore)
/tmp/worksync rollback v1
/tmp/worksync up
/tmp/worksync exec -- cat /workspace/HOST.txt
```

### 2.2 双机直连(真正两台机器)

1. 两端都开 Remote Login。
2. A 机:worksync remote add origin ssh://userB@hostB/~/worksync-store 后 push。
3. B 机:同样 remote add(指向自己的 ~/worksync-store),pull 即可。
4. 需免密时把 A 的 ~/.ssh/id_ed25519.pub 加入 B 的 ~/.ssh/authorized_keys。

### 2.3 冲突演练(CAS)

```bash
# 两机同时 push 不同提交 → 后 push 者应报 WB_CONFLICT
#   remote origin has diverged (remote xxx, local yyy); pull first
# pull 后重试即可;ref 用 .partial-cas + rename 原子写,CAS 双检防覆盖。
```

## 3. 常见问题

| 现象 | 原因 | 处理 |
|------|------|------|
| worksync: no worksync.yaml found | 规格文件仍是旧名 workbox.yaml | mv workbox.yaml worksync.yaml |
| commit: restic backup produced no snapshot id | 旧二进制(非 --json 解析) | 重新 go build |
| rollback 后文件出现在卷下的 tmp/... | 旧二进制(无 staging 重定位) | 重新 go build |
| pull: remote ref does not exist yet | 还没 push 过 | 先 push origin |
| ssh: Connection refused | Remote Login 未开启 | 见上方 2.0 |
| podman: image not known | create 用了 manifest-list digest | 代码已用 {{.Id}} 修复,重建二进制 |

## 4. 每轮 E2E 的卫生习惯

```bash
# 收尾:停掉测试容器、清理临时克隆,避免旧状态干扰下一轮
/tmp/worksync stop
rm -rf /tmp/wbclone
```
