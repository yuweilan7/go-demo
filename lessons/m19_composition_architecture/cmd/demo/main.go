// demo 是应用入口：选择配置来源、调用 composition root、输出可复核实验事实。
package main

import (
	"fmt"
	"os"

	"go-demo/lessons/m19_composition_architecture/internal/config"
	"go-demo/lessons/m19_composition_architecture/internal/lab"
)

// run 的 error 交回 main，所有实验 defer 已完成后才允许 main os.Exit。
// 默认文件中的 token 是公开教学值；本地秘密可用 M19_CONFIG 指向已忽略 .secrets 文件。
func run() error {
	path := os.Getenv("M19_CONFIG")
	if path == "" {
		path = "testdata/config.json"
	}
	cfg, err := config.Load(path, os.LookupEnv)
	if err != nil {
		return err
	}
	return lab.Run(cfg, os.Stdout)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "M19 failed:", err)
		os.Exit(1)
	}
}
