package gormcourse

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm/logger"
)

// sqlTrace 实现GORM logger.Interface；一个实例被owner和锁等待worker共享。
// mutex保护输出与计数，避免race。这里只打印本课合成SQL，连接配置永不传给logger。
// fc返回的是GORM为阅读而插值的SQL，不是网络packet；参数化原式由DryRun另行展示。
type sqlTrace struct {
	mu      sync.Mutex
	out     io.Writer
	selects int
}

func (t *sqlTrace) LogMode(logger.LogLevel) logger.Interface { return t }
func (t *sqlTrace) Info(context.Context, string, ...any)     {}
func (t *sqlTrace) Warn(context.Context, string, ...any)     {}
func (t *sqlTrace) Error(context.Context, string, ...any)    {}

// Trace 在每条GORM SQL完成时由processor调用，返回的rows不等于事务已提交。
// 不打印耗时猜调度，只记录实际SQL/行数/error；ErrMissingWhere可能在发送前被拦截。
func (t *sqlTrace) Trace(_ context.Context, _ time.Time, fc func() (string, int64), err error) {
	query, rows := fc()
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT") {
		t.selects++
	}
	fmt.Fprintf(t.out, "[SQL展示] rows=%d error=%v %s\n", rows, err, query)
}

// count 给关联实验读一个计数快照；前后差值数的是实际完成的SELECT次数。
// mutex没有锁住SQL本身，只保护本进程观察数据；这里没有同时执行其他实验。
func (t *sqlTrace) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.selects
}
