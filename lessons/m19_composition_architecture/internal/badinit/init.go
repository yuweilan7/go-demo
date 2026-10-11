// Package badinit 只由 cmd/initbad 导入。正常 cmd/demo 不导入它，避免坏例子污染正常启动。
package badinit

import (
	"errors"
	"net"
	"os"
)

var Listener net.Listener
var OpenError error
var ObservedVersion string

// init 故意在 main 解析/校验配置之前绑定端口，且只能把错误存在全局变量。
// 这是有界坏例子：本包不启动 Serve goroutine；cmd/initbad 会显式接管并关闭 Listener。
// 它证明 import 已经产生副作用，不把 “init 有顺序” 误推成 “init 管理资源生命周期”。
func init() {
	ObservedVersion = os.Getenv("M19_VERSION")
	Listener, OpenError = net.Listen("tcp", "127.0.0.1:0")
}

// Close 由坏例子 main 的 defer 调用；原打开错误和关闭错误同时保存。
func Close() error {
	if Listener == nil {
		return OpenError
	}
	return errors.Join(OpenError, Listener.Close())
}
