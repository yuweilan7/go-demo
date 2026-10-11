#!/usr/bin/env bash
# 后续 M20/M28/M29 安装者在授权服务器运行；M00 只交付并做 bash -n，不获取远端锁。
# 用法：bash with-install-lock.sh ROOT m20 RUN 900 -- bash install-mysql.sh
# ROOT 必须是已核对的学习根，control 已由安装者准备，不能指向 DeepHelp 根。
set -Eeuo pipefail

if [[ $# -lt 6 || "$5" != "--" ]]; then
    printf 'usage: with-install-lock.sh ROOT mXX RUN TIMEOUT_SECONDS -- COMMAND [ARGS...]\n' >&2
    exit 64
fi
root=$(realpath -e -- "$1")
case "$root" in
    /|/srv/deephelp*)
        printf 'refusing system root or protected DeepHelp root\n' >&2
        exit 64
        ;;
esac
module=$2
run=$3
budget=$4
shift 5
if [[ ! "$module" =~ ^m[0-9]{2}$ || ! "$run" =~ ^[a-z0-9][a-z0-9_-]{0,39}$ || ! "$budget" =~ ^[0-9]+$ ]]; then
    printf 'invalid module/run/budget\n' >&2
    exit 64
fi
if (( budget < 1 || budget > 1800 )); then
    printf 'budget must be between 1 and 1800 seconds\n' >&2
    exit 64
fi
control="$root/control"
if [[ ! -d "$control" || -L "$control" || ! -O "$control" || -L "$control/install.lock" ]]; then
    printf 'control must be an existing owned ordinary directory; lock must not be a symlink\n' >&2
    exit 64
fi
for tool in flock setsid timeout; do
    command -v "$tool" >/dev/null || { printf 'missing tool: %s\n' "$tool" >&2; exit 69; }
done

# fd 9 引用固定 lock inode；flock 是内核互斥锁，不能通过删除该文件“抢锁”。
# >> 不截断其他进程正在持有的 lock 文件。拿不到锁就退出 75，调用者继续独立课程。
umask 077
exec 9>>"$control/install.lock"
if ! flock -n 9; then
    printf 'installation lock busy; read install-owner.json and contact its owner\n' >&2
    exit 75
fi
child=''
owner="$control/install-owner.json"
temporary="$control/.install-owner.$$"
cleanup() {
    # 只通知本脚本创建的独立进程组。先等待命令退出，再移除 owner，最后释放锁。
    # 不能按端口、容器模糊名字或全服务器 PID 杀共享进程。
    if [[ -n "$child" ]] && kill -0 -- "-$child" 2>/dev/null; then
        kill -TERM -- "-$child" 2>/dev/null || true
        # 观察进程组是否仍存在，而不是把固定 sleep 当成“已结束”。最多观察 10 秒。
        for (( observation=0; observation<100; observation++ )); do
            kill -0 -- "-$child" 2>/dev/null || break
            sleep 0.1
        done
        if kill -0 -- "-$child" 2>/dev/null; then
            kill -KILL -- "-$child" 2>/dev/null || true
        fi
        wait "$child" || true
    fi
    rm -f -- "$temporary"
    if [[ -n "$child" ]] && kill -0 -- "-$child" 2>/dev/null; then
        printf 'owned command group still exists; owner metadata retained for diagnosis\n' >&2
    else
        rm -f -- "$owner"
    fi
    # 不显式 flock -u：如果异常子进程仍持有继承 fd，它仍保持互斥直到真正退出。
    # 关闭父进程 fd 不会把一个仍运行的安装者提前宣称为“锁已释放”。
    exec 9>&-
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

# owner 只含允许的模块/run、PID、时间、范围，不保存安装命令或凭据。
# 先写私有临时文件再 mv，在已经持锁的范围里发布可读的 owner 事实。
printf '{"module":"%s","run":"%s","pid":%d,"started_at_utc":"%s","scope":"infrastructure changes"}\n' \
    "$module" "$run" "$$" "$(date -u +%FT%TZ)" >"$temporary"
mv -f -- "$temporary" "$owner"

# 安装者传入的 COMMAND 直接作为参数执行，不使用 eval，也不打印命令（可能含秘密）。
# setsid 给它独立进程组，timeout 限制上限；命令必须同步等待安装和语义核验完成。
# 正常退出/可捕捉信号会执行 cleanup。SIGKILL 无法执行 trap：内核锁在最后持有
# fd 的进程退出时释放，owner 可能陈旧；必须核对 PID/锁而不能据时间手删活动锁。
setsid timeout --signal=TERM --kill-after=10s "${budget}s" "$@" &
child=$!
set +e
wait "$child"
status=$?
set -e
exit "$status"
