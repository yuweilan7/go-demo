package gormcourse

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrJoin = errors.New("任务最终Done未确认")

// workResult区分工作与资源错误：预期DeadlineExceeded不能顺便掩盖Conn.Close失败。
// ownedTask由cancelCase/test创建并拥有。result容量1让owner提前返回时发送也能完成；
// done只能wrapper关闭，代表工作函数和wrapper的最终清理都已结束。
type workResult struct{ Err, CleanupErr error }
type ownedTask struct {
	result         chan workResult
	done           chan struct{}
	cancel         context.CancelFunc
	wrapperCleanup error
	pending        *workResult
	joined         bool
}

// startOwned创建任务即返回完整登记表。fn拥有它借到的Conn/Tx，wrapper拥有自己的
// cancel/cleanup/done。caller必须立刻登记stopAndJoin，不能只取Result就遗弃任务。
func startOwned(ctx context.Context, fn func(context.Context) workResult, cleanup func() error) *ownedTask {
	workCtx, cancel := context.WithCancel(ctx)
	t := &ownedTask{result: make(chan workResult, 1), done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(t.done) // 最后执行：此前cleanup与cancel先完成，close形成观察同步点。
		defer cancel()
		res := func() (res workResult) {
			defer func() {
				if value := recover(); value != nil {
					if cause, ok := value.(error); ok {
						res.Err = fmt.Errorf("worker panic: %w", cause)
					} else {
						res.Err = fmt.Errorf("worker panic: %v", value)
					}
				}
			}()
			return fn(workCtx)
		}()
		t.result <- res // 这里只交工作结果，cleanup可能尚未执行，不能把它当最终Done。
		if cleanup != nil {
			t.wrapperCleanup = cleanup()
		}
	}()
	return t
}

// join先等真实done，之后才读取wrapperCleanup与result；channel关闭建立happens-before。
// 已读result的测试可保存pending，但仍要等done。joined/pending只有owner goroutine读写。
func (t *ownedTask) join(ctx context.Context) (workResult, error) {
	select {
	case <-t.done:
		if t.pending == nil {
			res := <-t.result
			t.pending = &res
		}
		t.joined = true
		return *t.pending, t.wrapperCleanup
	case <-ctx.Done():
		return workResult{}, errors.Join(ErrJoin, ctx.Err())
	}
}

// stopAndJoin用于owner所有提前失败出口：先发取消通知，再用独立Background预算等退出。
// errors.Join保留原失败、工作、工作清理、wrapper清理、join失败，不能互相覆盖。
func (t *ownedTask) stopAndJoin(original error) error {
	t.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	res, joinedErr := t.join(ctx)
	return errors.Join(original, res.Err, res.CleanupErr, joinedErr)
}
