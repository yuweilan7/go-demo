"""接入范围回归断言；调用拦截只证明没有访问，不替代真实 Redis/PG 核验。"""

import socket
import unittest
from unittest import mock

import redis

import check_infra


class RedisScopeTests(unittest.TestCase):
    def test_host_outside_explicit_loopback_scope_rejected_before_resources(self):
        """unittest 调用；合成配置绝不拨号，拦截对象独立观察 constructor/PING/INFO。"""
        # client 是本测试的观察替身：若发生错误调用，仅记录调用，不建立任何连接池。
        # 再拦截 socket，保证即使范围判断回归，运行这个测试也没有网络副作用。
        client = mock.Mock()
        with (
            mock.patch.object(redis, "Redis", return_value=client) as constructor,
            mock.patch.object(check_infra, "tcp_check", return_value={"pass": False, "error_type": "NonLoopbackRejected"}) as tcp,
            mock.patch.object(socket, "create_connection", side_effect=AssertionError("scope test must never dial")) as socket_dial,
        ):
            # ::1 虽是 IPv6 loopback，也不在本课显式 127.0.0.1 转发配置的范围内；
            # localhost 需要名字解析，不能把别名隐式扩成当前已验证的 IPv4 入口。
            for host in ("198.51.100.7", "localhost", "::1", ""):
                with self.subTest(host=host):
                    # username/password 是不会使用的合成占位值，既不加载真实 secret，
                    # 也不测试鉴权；目的只在证明拒绝发生在资源/协议之前。
                    connection = {"REDIS_HOST": host, "REDIS_PORT": "6379", "REDIS_USER": "synthetic-unused"}
                    result = check_infra.redis_check(connection, {"REDIS_PASSWORD": "synthetic-unused"})

                    # 失败记录必须表明“未创建/未执行”，不能伪称执行后已关闭资源。
                    self.assertEqual(result["error_type"], "NonLoopbackRejected")
                    self.assertFalse(result["tcp"]["executed"])
                    self.assertFalse(result["resources_created"])
                    self.assertFalse(result["protocol_attempted"])
                    self.assertFalse(result["authenticated"])
                    self.assertIn("were not created", result["cleanup"])

                    # 这些次数来自独立拦截点，证明真实代码没有尝试创建客户端/访问协议。
                    # tcp_check 即使只返回 False 也不能充当 guard，本实现应根本不调用它。
                    constructor.assert_not_called()
                    client.ping.assert_not_called()
                    client.info.assert_not_called()
                    client.close.assert_not_called()
                    client.connection_pool.disconnect.assert_not_called()
                    tcp.assert_not_called()
                    socket_dial.assert_not_called()


if __name__ == "__main__":
    unittest.main()
