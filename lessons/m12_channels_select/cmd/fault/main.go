// fault 是显式危险入口。只有传入 pointer-race 才实际破坏所有权，普通 demo/test 不调用它。
package main

import (
	"fmt"
	"os"
	"sync"

	"go-demo/lessons/m12_channels_select/internal/course"
)

// main 由 go run -race ./cmd/fault pointer-race 启动。工作量固定为两个 20,000 次循环，
// 不产生无限 goroutine；race detector 报告后该进程由 go 工具回收，不影响正常课程入口。
func main() {
	if len(os.Args) != 2 || os.Args[1] != "pointer-race" {
		fmt.Fprintln(os.Stderr, "显式故障用法: go run -race ./cmd/fault pointer-race")
		os.Exit(2)
	}
	pointerRace()
}

// pointerRace 演示发送指针以后，发送方继续修改指向对象与接收方读取之间没有同步关系。
// ready 只让 reader 到达循环前，与 writer 共同开始；它没有在每次对象访问之间排定先后。
// WaitGroup 等待两者完成以回收 goroutine，却不能倒过来让此前冲突的读写变得安全。
func pointerRace() {
	pointer := &course.Order{ID: "O-01", AmountCents: 100}
	channel := make(chan *course.Order, 1)
	ready := make(chan struct{})
	var tasks sync.WaitGroup
	var observed int
	channel <- pointer // 仅复制地址；初始化的 100 可见，不授权后续无同步的并发写。
	tasks.Add(2)
	go func() {
		defer tasks.Done()
		owned := <-channel
		close(ready)
		total := 0
		for index := 0; index < 20_000; index++ {
			total += owned.AmountCents // 危险读：另一个 goroutine 正在写同一内存位置。
		}
		observed = total
	}()
	go func() {
		defer tasks.Done()
		<-ready
		for index := 0; index < 20_000; index++ {
			pointer.AmountCents = index // 危险写：发送完成没有建立这次写与 reader 这次读之间的关系。
		}
	}()
	tasks.Wait()
	fmt.Printf("故障循环已结束 observed=%d；数值没有保证，必须读取 race 报告\n", observed)
	fmt.Println("修复: 发送值副本，或 sender 停止访问对象并等待 receiver 经 channel 回传所有权")
}
