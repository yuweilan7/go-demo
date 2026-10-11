package main

import (
	"context"
	"fmt"
	"go-demo/lessons/m32_framework_source_reading/internal/lab"
	"os"
	"time"
)

// run拥有课程总预算，lab负责各资源的独立收尾；只有run的defer已执行后main才可能os.Exit。
func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := lab.Run(ctx, os.Stdout)
	return err
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
