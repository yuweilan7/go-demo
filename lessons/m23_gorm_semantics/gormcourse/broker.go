package gormcourse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// broker 保存本run唯一队列和AMQP连接；它的owner是Run，hook仅借用publish。
// 没有应用consumer goroutine：同步basic.get即可证明事件真的存在于远端broker。
// socket保留在这里，让失败清理也能设独立I/O上限，不能依赖已取消的业务ctx。
type broker struct {
	conn   *amqp.Connection
	ch     *amqp.Channel
	socket *boundedConn
	queue  string
	owned  bool
	out    io.Writer
}

// openBroker 由Run调用。先建立连接即登记失败保护，再核对随机队列不存在。
// passive失败会关闭probe channel，所以探测和业务操作使用不同channel。
func openBroker(ctx context.Context, c config, queue string, out io.Writer) (b *broker, err error) {
	b = &broker{queue: queue, out: out}
	defer func() {
		if err != nil {
			err = errors.Join(err, b.close())
		}
	}()
	u := amqp.URI{Scheme: "amqp", Host: c.rabbit.Host, Port: c.rabbit.Port, Username: c.mqSecret.Username, Password: c.mqSecret.Password, Vhost: c.mqSecret.Vhost}
	b.conn, err = amqp.DialConfig(u.String(), amqp.Config{Heartbeat: 5 * time.Second, Dial: func(network, addr string) (net.Conn, error) {
		// AMQP库创建reader/heartbeat等内部任务；我们拥有这一个连接的Close权。
		// DialContext限制连接建立，socket总deadline限制basic.get/delete等无ctx方法。
		socket, e := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
		if e != nil {
			return nil, e
		}
		b.socket = &boundedConn{Conn: socket, budget: time.Now().Add(75 * time.Second)}
		if e = b.socket.SetDeadline(time.Time{}); e != nil {
			return nil, errors.Join(e, socket.Close())
		}
		return b.socket, nil // 库拿到装饰器，握手/heartbeat不能清掉owner的总预算。
	}})
	if err != nil {
		return b, errors.New("AMQP握手失败（接入URI不输出）")
	}
	if err = b.absent(); err != nil {
		return b, err
	}
	b.ch, err = b.conn.Channel()
	if err != nil {
		return b, err
	}
	b.owned = true // 已核对不存在；创建结果不明确时也只精确清理这个随机名字。
	// RabbitMQ4.3默认禁止transient_nonexcl_queues，独占名字使用durable队列再精确删除。
	// durable只描述队列定义，不把本课delivery自动变成persistent消息。
	_, err = b.ch.QueueDeclare(queue, true, false, false, false, nil)
	if err != nil {
		return b, err
	}
	if err = b.ch.Confirm(false); err != nil {
		return b, err
	}
	fmt.Fprintf(out, "[AMQP.namespace] queue=%s exchange=已有默认交换机 group=无 records<=2\n", queue)
	return b, nil
}

// absent 使用真实queue.declare(passive)读回对象存在性，不拿本地字段当清理证据。
// RabbitMQ的404精确代表本vhost没有该队列，预期404已关闭probe，无需再Close它。
func (b *broker) absent() (err error) {
	probe, err := b.conn.Channel()
	if err != nil {
		return err
	}
	_, err = probe.QueueDeclarePassive(b.queue, true, false, false, false, nil)
	var protocolErr *amqp.Error
	if errors.As(err, &protocolErr) && protocolErr.Code == 404 {
		return nil
	}
	if !probe.IsClosed() {
		err = errors.Join(err, probe.Close())
	}
	if err == nil {
		return errors.New("拒绝复用已存在的随机AMQP队列")
	}
	return err
}

// publish 在hook或relay的调用栈内同步完成AMQP publish和publisher confirm。
// publish返回只证明客户端写出；WaitContext得到Ack才证明broker确认，不证明SQL提交。
// v1.15.0的WithContext只在publish开始前检查取消，不打断已经进行的socket写入；
// 在途I/O靠socket deadline限制，WaitContext才能在等待确认时观察ctx通知。
func (b *broker) publish(ctx context.Context, id string) error {
	confirmation, err := b.ch.PublishWithDeferredConfirmWithContext(ctx, "", b.queue, false, false, amqp.Publishing{ContentType: "text/plain", MessageId: id, Body: []byte(id)})
	if err != nil {
		return err
	}
	if confirmation == nil {
		return errors.New("AMQP confirm模式没有返回确认对象")
	}
	ack, err := confirmation.WaitContext(ctx)
	if err != nil {
		return err
	}
	if !ack {
		return errors.New("broker Nack")
	}
	fmt.Fprintf(b.out, "[AMQP.confirm] id=%s ack=true\n", id)
	return nil
}

// take 从真正的broker取一条消息，验证ID和body，然后发送消费者Ack。
// basic.get-empty是协议空结果；与Slice长度、HTTP200或publisher confirm分开证明。
func (b *broker) take(want string) error {
	d, ok, err := b.ch.Get(b.queue, false)
	if err != nil {
		return err
	}
	if !ok || d.MessageId != want || string(d.Body) != want {
		return fmt.Errorf("真实AMQP投递不符合预期 id=%q exists=%v", d.MessageId, ok)
	}
	if err = d.Ack(false); err != nil {
		return err
	}
	fmt.Fprintf(b.out, "[AMQP.get/ack] id=%s body=%s ack-sent=true\n", d.MessageId, d.Body)
	return b.empty() // 同channel的下一basic.get RPC读回空，Ack在它之前按协议顺序发送。
}

// empty 给回滚修复路径确认没有发布；它不偷偷Ack非预期消息以制造空结果。
func (b *broker) empty() error {
	d, ok, err := b.ch.Get(b.queue, false)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("队列出现非预期消息: %s", d.MessageId)
	}
	fmt.Fprintln(b.out, "[AMQP.empty] basic.get-empty=true")
	return nil
}

// close 是AMQP唯一owner的最终清理：独立socket预算、精确delete、协议读回404、
// 再关闭channel/connection。具名err用Join保留delete/读回/Close任一失败。
func (b *broker) close() (err error) {
	if b == nil {
		return nil
	}
	if b.socket != nil {
		err = errors.Join(err, b.socket.resetBudget(time.Now().Add(8*time.Second)))
	}
	if b.conn == nil {
		if b.socket != nil {
			err = errors.Join(err, b.socket.Close())
		}
		return err
	}
	if b.owned {
		ch := b.ch
		if ch == nil || ch.IsClosed() {
			var e error
			ch, e = b.conn.Channel()
			err = errors.Join(err, e) // 不覆盖之前设置清理budget的失败。
		}
		if ch != nil && !ch.IsClosed() {
			_, e := ch.QueueDelete(b.queue, false, false, false)
			err = errors.Join(err, e)
			if e == nil {
				e = b.absent()
				err = errors.Join(err, e)
				if e == nil {
					fmt.Fprintf(b.out, "[cleanup.AMQP] exact_queue=%s passive404=true\n", b.queue)
				}
			}
			if ch != b.ch {
				err = errors.Join(err, ch.Close())
			}
		}
	}
	if b.ch != nil && !b.ch.IsClosed() {
		err = errors.Join(err, b.ch.Close())
	}
	if !b.conn.IsClosed() {
		err = errors.Join(err, b.conn.Close())
	}
	return err
}
