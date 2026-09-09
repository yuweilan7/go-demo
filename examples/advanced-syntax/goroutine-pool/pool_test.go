package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolNeverExceedsCapacity(t *testing.T) {
	const (
		capacity  = 3
		taskCount = 12
	)
	pool := mustNewPool(capacity, taskCount)

	var active atomic.Int64
	var maxActive atomic.Int64
	started := make(chan struct{}, taskCount)
	release := make(chan struct{})
	tasks := make([]*Task, 0, taskCount)

	for i := 0; i < taskCount; i++ {
		task := NewTask("work", func() error {
			current := active.Add(1)
			updateMaximum(&maxActive, current)
			defer active.Add(-1)
			started <- struct{}{}
			<-release
			return nil
		})
		if err := pool.Submit(task); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
	}

	for i := 0; i < capacity; i++ {
		<-started
	}
	if got := active.Load(); got != capacity {
		t.Fatalf("active tasks = %d, want %d", got, capacity)
	}

	close(release)
	pool.CloseAndWait()

	if got := maxActive.Load(); got != capacity {
		t.Fatalf("maximum active tasks = %d, want %d", got, capacity)
	}
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubmitBlocksOnlyAfterWorkersAndQueueAreFull(t *testing.T) {
	pool := mustNewPool(1, 1)
	release := make(chan struct{})
	started := make(chan struct{})

	first := NewTask("running", func() error {
		close(started)
		<-release
		return nil
	})
	if err := pool.Submit(first); err != nil {
		t.Fatal(err)
	}
	<-started

	second := NewTask("queued", func() error { return nil })
	if err := pool.Submit(second); err != nil {
		t.Fatal(err)
	}

	third := NewTask("blocked submit", func() error { return nil })
	returned := make(chan error, 1)
	go func() {
		returned <- pool.Submit(third)
	}()

	select {
	case err := <-returned:
		t.Fatalf("Submit returned while worker and queue were full: %v", err)
	case <-time.After(50 * time.Millisecond):
		// 预期路径：释放正在执行的任务之前，没有位置接收 third。
	}

	close(release)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit did not return after a worker became available")
	}

	pool.CloseAndWait()
}

func TestTaskErrorAndClosedPool(t *testing.T) {
	pool := mustNewPool(1, 1)
	want := errors.New("boom")
	task := NewTask("failure", func() error { return want })

	if err := pool.Submit(task); err != nil {
		t.Fatal(err)
	}
	pool.CloseAndWait()

	if got := task.Wait(); !errors.Is(got, want) {
		t.Fatalf("task error = %v, want %v", got, want)
	}
	if err := pool.Submit(NewTask("late", func() error { return nil })); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Submit after close = %v, want %v", err, ErrPoolClosed)
	}
}
