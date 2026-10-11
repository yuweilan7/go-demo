// initbad 是单独运行的故障入口，只有它导入那个偷绑端口的包。
package main

import (
	"errors"
	"fmt"
	"os"

	"go-demo/lessons/m19_composition_architecture/internal/badinit"
)

// run 让错误返回到 main；这样 defer 会先执行，main 最后才 os.Exit，不跳过资源清理。
func run() (err error) {
	defer func() {
		err = errors.Join(err, badinit.Close()) // 返回原因与关闭原因都保留，defer不覆盖先前错误。
		fmt.Println("initbad.owner.closed=true")
	}()
	if badinit.OpenError != nil {
		return badinit.OpenError
	}
	if err := os.Setenv("M19_VERSION", "main-configured"); err != nil {
		return err
	}
	fmt.Printf("initbad.bound_before_main=true observed_version=%q main_version=%q\n", badinit.ObservedVersion, os.Getenv("M19_VERSION"))
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
