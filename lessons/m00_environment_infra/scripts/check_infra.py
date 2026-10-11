"""M00 真实只读核验：TCP、鉴权、版本与资源盘点分别记录，不做业务写读。"""

import argparse
import contextlib
import datetime
import importlib.metadata
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import threading
import time


def read_env(path):
    """调用者提供既有文件；返回本次进程独占字典，不能打印这个对象。"""
    return {
        key: value
        for line in path.read_text(encoding="utf-8-sig").splitlines()
        if line and not line.startswith("#") and "=" in line
        for key, value in [line.split("=", 1)]
    }


def safe_failure(error):
    """异常正文可能含 DSN/口令；公开和私有报告都只保留类别及 SQLSTATE。"""
    result = {"error_type": type(error).__name__}
    sqlstate = getattr(error, "sqlstate", None)
    if sqlstate:
        result["sqlstate"] = sqlstate
    return result


def tcp_check(host, port):
    """真实等待最多两秒；with 的调用者拥有 socket，返回前关闭连接。"""
    if host != "127.0.0.1":
        return {"pass": False, "error_type": "NonLoopbackRejected"}
    try:
        with socket.create_connection((host, int(port)), timeout=2):
            return {"pass": True}
    except Exception as error:
        return {"pass": False, **safe_failure(error)}


def redis_check(connection, secret):
    """main 调用；先限制接入范围，再把既有口令交给本次独占的 redis-py 客户端。"""
    # tcp_check 的 pass=False 是证据结果，不能自动阻止下面的客户端代码。
    # 因此 host 的范围判断必须在导入驱动、创建 client/pool 和任何拨号之前返回。
    # 只接受显式 127.0.0.1，与本课 SSH 本机转发和 PG 的入口约定一致；不解析别名。
    if connection["REDIS_HOST"] != "127.0.0.1":
        return {
            "tcp": {"pass": False, "executed": False, "error_type": "NonLoopbackRejected"},
            "authenticated": False,
            "error_type": "NonLoopbackRejected",
            "resources_created": False,
            "protocol_attempted": False,
            "business_write_read": "not run",
            "persistent_recovery": "not run",
            "cleanup": "not required: Redis client and connection pool were not created",
        }

    import redis
    from redis.backoff import NoBackoff
    from redis.retry import Retry

    result = {"client_version": importlib.metadata.version("redis"), "tcp": tcp_check(connection["REDIS_HOST"], connection["REDIS_PORT"])}
    client = redis.Redis(
        host=connection["REDIS_HOST"], port=int(connection["REDIS_PORT"]),
        username=connection["REDIS_USER"], password=secret["REDIS_PASSWORD"],
        socket_connect_timeout=2, socket_timeout=2, decode_responses=True,
        retry=Retry(NoBackoff(), 0),
    )
    result["resources_created"] = True
    result["protocol_attempted"] = False
    try:
        # ping 返回 True 证明当前口令被协议接受。INFO 只读取版本，不读取业务数据。
        result["protocol_attempted"] = True
        result["authenticated"] = bool(client.ping())
        result["server_version"] = client.info("server")["redis_version"]
        result["readonly_commands"] = ["AUTH (client handshake)", "PING", "INFO server"]
    except Exception as error:
        result.update({"authenticated": False, **safe_failure(error)})
    finally:
        # 本探针 owns 这个独立连接池；close + disconnect 不接管 DeepHelp 的池或隧道。
        client.close()
        client.connection_pool.disconnect()
    result["business_write_read"] = "not run"
    result["persistent_recovery"] = "not run"
    result["cleanup"] = "client and its connection pool closed"
    return result


def postgres_check(path, local_port=None):
    """main 调用；使用既有 PG 配置，仅 SELECT 会话元信息，绝不读取正式业务表。"""
    import psycopg

    config = json.loads(path.read_text(encoding="utf-8-sig"))
    if local_port is not None:
        config = {**config, "host": "127.0.0.1", "port": local_port}
    result = {"client_version": importlib.metadata.version("psycopg"), "tcp": tcp_check(config["host"], config["port"])}
    if config["host"] != "127.0.0.1":
        return {**result, "authenticated": False, "error_type": "NonLoopbackRejected"}
    try:
        # default_transaction_read_only 从连接启动就限制这个会话，不能限制其他应用。
        # connect_timeout 限制连接，statement_timeout 限制 SELECT；此探针没有自动重试。
        with psycopg.connect(
            **config, connect_timeout=3, autocommit=True,
            options="-c statement_timeout=2000 -c lock_timeout=1000 -c default_transaction_read_only=on -c application_name=golearn_m00_readonly",
        ) as connection:
            with connection.cursor() as cursor:
                cursor.execute("SELECT current_setting('server_version'), current_setting('transaction_read_only')")
                version, readonly = cursor.fetchone()
                if readonly != "on":
                    raise RuntimeError("ReadOnlyContractFailed")
                result.update({"authenticated": True, "server_version": version, "transaction_read_only": readonly})
                result["readonly_commands"] = ["SELECT server_version, transaction_read_only (session metadata only)"]
        # 内层 cursor 先退出，外层 connection 再关闭；返回时无活动事务/后端连接由本课持有。
    except Exception as error:
        result.update({"authenticated": False, **safe_failure(error)})
    result["business_write_read"] = "not run"
    result["persistent_recovery"] = "not run"
    result["cleanup"] = "cursor and connection contexts exited"
    return result


def ssh_arguments(root, connection):
    """所有 SSH 路径共用严格校验入口；返回值不能打印，因其中含私有连接定位信息。"""
    return [
        "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
        "-o", f"UserKnownHostsFile={root / 'infra/client/known_hosts'}",
        "-o", "ConnectTimeout=5", "-o", "ConnectionAttempts=1",
        "-i", connection["DEEPHELP_SSH_KEY_WINDOWS"],
        "-p", connection["DEEPHELP_SSH_PORT"],
        f"{connection['DEEPHELP_SSH_USER']}@{connection['DEEPHELP_SSH_HOST']}",
    ]


@contextlib.contextmanager
def owned_tunnel(root, connection):
    """仅 --temporary-tunnel 调用；拥有自己的一条 SSH 进程和两个临时 loopback 端口。"""
    ports = []
    reservations = []
    try:
        for _ in range(2):
            reservation = socket.socket()
            reservation.bind(("127.0.0.1", 0))
            ports.append(reservation.getsockname()[1])
            reservations.append(reservation)
    finally:
        # 端口交给 ssh 前释放保留 socket。分配与 ssh bind 有窗口；ExitOnForwardFailure
        # 负责拒绝被别人抢先占用的端口，绝不因冲突去杀那个进程。
        for reservation in reservations:
            reservation.close()
    arguments = ssh_arguments(root, connection)
    arguments[-1:-1] = [
        "-N", "-T", "-o", "ExitOnForwardFailure=yes",
        "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=1",
        "-L", f"127.0.0.1:{ports[0]}:127.0.0.1:6379",
        "-L", f"127.0.0.1:{ports[1]}:127.0.0.1:15432",
    ]
    process = subprocess.Popen(arguments, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
    try:
        deadline = time.monotonic() + 7
        observer = threading.Event()
        for _ in range(100):
            if process.poll() is not None:
                raise RuntimeError("OwnedTunnelExitedBeforeReady")
            if all(tcp_check("127.0.0.1", port)["pass"] for port in ports):
                yield {"redis": ports[0], "postgres": ports[1], "owner_pid": process.pid}
                break
            if time.monotonic() >= deadline:
                raise TimeoutError("OwnedTunnelReadinessTimeout")
            # 这是有 deadline 的重复状态观察；TCP 成功才是屏障，不把等待 50ms 当就绪证明。
            observer.wait(0.05)
        else:
            raise TimeoutError("OwnedTunnelReadinessAttemptsExceeded")
    finally:
        # 只停止我们保存的 Popen 对象；从不通过端口/PID 模糊扫描管理别人的隧道。
        process.terminate()
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)
        if any(tcp_check("127.0.0.1", port)["pass"] for port in ports):
            raise RuntimeError("OwnedTunnelPortStillListening")


def ssh_inventory(root, connection):
    """沿既有 SSH key/known_hosts 做固定只读盘点；不打印目标 IP、密钥正文或环境。"""
    command = "set -eu; printf 'KERNEL\\n'; uname -sm; printf 'CPU\\n'; nproc; printf 'MEMORY_MIB\\n'; free -m; printf 'DISK_MIB\\n'; df -Pm /srv; printf 'LISTENERS\\n'; ss -H -lnt | awk '{print $4}'; printf 'CONTAINERS\\n'; sudo -n docker ps -a --format '{{.Names}}|{{.Image}}|{{.Status}}|{{.Ports}}'; printf 'RESOURCE_USE\\n'; timeout 5 sudo -n docker stats --no-stream --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}'; printf 'LOCK_TOOLS\\n'; command -v flock setsid timeout; flock --version; bash --version | head -n 1"
    arguments = ssh_arguments(root, connection) + [command]
    try:
        completed = subprocess.run(arguments, capture_output=True, text=True, timeout=20, check=False)
        if completed.returncode != 0:
            # ssh stderr 可能给出私有主机路径；保留退出码，必要时私下定位，不泄漏原文。
            return {"pass": False, "exit_code": completed.returncode, "error_type": "SSHCommandFailed"}
        # 把本课锁脚本送给 bash -n：仅解析，不执行 mkdir/flock/安装命令，不写远端文件。
        lock_script = Path(__file__).with_name("with-install-lock.sh").read_text(encoding="utf-8")
        # Windows text 管道会把 LF 转成 CRLF；bash 的脚本 stdin 必须保留 LF 字节。
        syntax = subprocess.run(arguments[:-1] + ["bash -n"], input=lock_script.encode("utf-8"), capture_output=True, timeout=10, check=False)
        return {"pass": syntax.returncode == 0, "strict_host_key": True, "read_only_inventory": completed.stdout.splitlines(), "lock_script_bash_n_exit": syntax.returncode, "lock_runtime": "not executed: M00 remote scope is read only"}
    except Exception as error:
        return {"pass": False, **safe_failure(error)}


def main():
    """拥有整次核验；各层失败单独记录，最终状态不能用某一端口成功覆盖另一个失败。"""
    parser = argparse.ArgumentParser()
    parser.add_argument("--deephelp-root", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--skip-ssh", action="store_true")
    parser.add_argument("--temporary-tunnel", action="store_true")
    args = parser.parse_args()
    # 参数与配置字典只在本进程使用，不存入报告。报告不包含密码、DSN、私钥和 IP。
    connection = read_env(args.deephelp_root / "infra/config/connections.env")
    secret = read_env(Path(os.environ.get("DEEPHELP_SECRET_FILE", str(Path.home() / ".deephelp/.env.secret"))))
    report = {"observed_at_utc": datetime.datetime.now(datetime.UTC).isoformat(), "python_version": sys.version.split()[0], "scope": "bounded read-only; no business writes, service mutation or installations"}
    tunnel_scope = owned_tunnel(args.deephelp_root, connection) if args.temporary_tunnel else contextlib.nullcontext(None)
    with tunnel_scope as ports:
        redis_connection = connection if ports is None else {**connection, "REDIS_HOST": "127.0.0.1", "REDIS_PORT": str(ports["redis"])}
        for name, check in [
            ("redis", lambda: redis_check(redis_connection, secret)),
            ("postgres", lambda: postgres_check(args.deephelp_root / ".local/m30/live-postgres.json", None if ports is None else ports["postgres"])),
        ]:
            try:
                report[name] = check()
            except Exception as error:
                report[name] = {"authenticated": False, **safe_failure(error)}
        report["ssh"] = {"pass": None, "scope": "skipped"} if args.skip_ssh else ssh_inventory(args.deephelp_root, connection)
        if ports is not None:
            report["owned_tunnel"] = ports
    if args.temporary_tunnel:
        report["owned_tunnel"]["cleanup"] = "owner process waited; both temporary local ports no longer listening"
    args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if all(report[name].get("authenticated") is True for name in ("redis", "postgres")) and report["ssh"].get("pass") is not False else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        # 最外层也不让 traceback 把敏感配置/异常正文带到屏幕。
        print(json.dumps({"pass": False, **safe_failure(error)}))
        raise SystemExit(1)
