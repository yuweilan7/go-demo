package lab

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	b "go-demo/lessons/m37_order_backend_capstone/orderbackend"
)

// rebalance 在已处理完原事件后创建一个同group只读standby。没有新消息，standby
// 只Poll/AllowRebalance，绝不ProcessRecord/CommitRecords；没有第二业务写者。
// 实际broker成员1→2→1和真实client generation/assignment证据一起证明协调发生。
func rebalance(ctx context.Context, s *session, a *b.App) (err error) {
	r := s.business
	observeMembers := func(n int) error {
		return b.Observe(ctx, func() (bool, error) {
			gs, e := r.Broker.Admin.DescribeGroups(ctx, r.Broker.Group)
			if e != nil {
				return false, e
			}
			g, ok := gs[r.Broker.Group]
			if !ok || g.Err != nil {
				return false, g.Err
			}
			if g.State == "Stable" && len(g.Members) == n {
				fmt.Fprintf(s.out, "REBALANCE_BROKER state=%s members=%d detail=%+v\n", g.State, len(g.Members), g.Members)
				return true, nil
			}
			return false, nil
		})
	}
	if e := observeMembers(1); e != nil {
		return e
	}
	member, oldGen := a.Consumer.K.GroupMetadata()
	fmt.Fprintf(s.out, "REBALANCE_OLD member=%s generation=%d\n", member, oldGen)
	assigned := make(chan struct{}, 1)
	opts := append(b.ConsumerOptions(r.Broker.Topic, r.Broker.Group), kgo.OnPartitionsAssigned(func(_ context.Context, c *kgo.Client, partitions map[string][]int32) {
		member, gen := c.GroupMetadata()
		fmt.Fprintf(s.out, "STANDBY_ASSIGNMENT member=%s generation=%d partitions=%v commits_disabled=true\n", member, gen, partitions)
		select {
		case assigned <- struct{}{}:
		default:
		}
	}))
	c, e := b.NewKafkaClient(s.cfg, r.Namespace+"-readonly-standby", opts...)
	if e != nil {
		return e
	}
	s.clients = append(s.clients, c)
	life, cancel := context.WithCancel(ctx)
	j := launch(life, func(ctx context.Context) error {
		for {
			f := c.K.PollRecords(ctx, 1)
			if ctx.Err() != nil {
				return nil
			}
			if es := f.Errors(); len(es) > 0 {
				return es[0].Err
			}
			if len(f.Records()) > 0 {
				return errors.New("standby不应读到未处理业务，不能提交")
			}
			c.K.AllowRebalance()
		}
	})
	s.jobs = append(s.jobs, j)
	// guard在观察或断言失败时也先停止poll/join，再Close client。不能先Close
	// 一个仍被外层worker使用的client然后把Close完成当worker退出。
	defer func() {
		cancel()
		j.cancel()
		err = errors.Join(err, b.JoinDone(j.done, "standby poll worker", 10*time.Second))
		select {
		case <-j.done:
			err = errors.Join(err, j.err, c.Close())
		default:
			err = errors.Join(err, b.ErrCleanup)
		}
	}()
	if e = await(ctx, assigned, "standby实际assignment回调"); e != nil {
		return e
	}
	if e = observeMembers(2); e != nil {
		return e
	}
	_, newGen := a.Consumer.K.GroupMetadata()
	_, standbyGen := c.K.GroupMetadata()
	if e = b.Assert(newGen > oldGen && standbyGen == newGen, "实际generation必须改变且同group一致 old=%d active=%d standby=%d", oldGen, newGen, standbyGen); e != nil {
		return e
	}
	cancel()
	j.cancel()
	if e = b.JoinDone(j.done, "standby最终Done", 10*time.Second); e != nil {
		return e
	}
	if j.err != nil {
		return j.err
	}
	if e = c.Close(); e != nil {
		return e
	}
	if e = observeMembers(1); e != nil {
		return e
	}
	_, lastGen := a.Consumer.K.GroupMetadata()
	if e = b.Assert(lastGen > newGen, "退出standby后的generation应再增加 actual=%d", lastGen); e != nil {
		return e
	}
	offset, e := r.Broker.Offset(ctx)
	if e != nil {
		return e
	}
	if e = b.Assert(offset == 3, "standby绝不移动已处理next3，actual=%d", offset); e != nil {
		return e
	}
	fmt.Fprintf(s.out, "REBALANCE_DONE generations=%d/%d/%d next=3 standby_commits=0 worker_done=true client_done=true\n", oldGen, newGen, lastGen)
	return nil
}
