package lab

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// writeGate 只截住本课 socket 的下一次 Write，用可观察屏障定位发送已开始。
// 它不是 broker 替身：解除屏障后原始字节继续送入真实 RabbitMQ。
type writeGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGate() *writeGate {
	return &writeGate{entered: make(chan struct{}), release: make(chan struct{})}
}

// open 由 owner 或 transport Close 调用；sync.Once 保证并发收尾不会重复 close panic。
func (g *writeGate) open() { g.once.Do(func() { close(g.release) }) }

// transport 包住 DialContext 返回的真实 TCP 连接。锁只保护 gate 的交接，
// 不在锁内等待 release；否则 Close 无法拿锁解除被堵住的 Write。
type transport struct {
	net.Conn
	mu     sync.Mutex
	gate   *writeGate
	active *writeGate
}

// arm 由context实验在拓扑/confirm设置完成后调用，只把下一次Write交给此屏障。
func (t *transport) arm(g *writeGate) { t.mu.Lock(); t.gate = g; t.mu.Unlock() }

// Write 被AMQP库的发送路径调用。b是库交来的帧切片，本函数不修改内容；
// gate的值是指针副本，与owner共享entered/release，只暂停自己保存的真实连接。
func (t *transport) Write(b []byte) (int, error) {
	t.mu.Lock()
	g := t.gate
	if g != nil {
		t.gate = nil
		t.active = g
	}
	t.mu.Unlock()
	if g != nil {
		close(g.entered) // owner 在这一刻知道真实 Publish 已进入底层写，而非猜调度。
		<-g.release
		t.mu.Lock()
		t.active = nil
		t.mu.Unlock()
	}
	return t.Conn.Write(b)
}

// Close 既关闭实际 socket，也释放自己制造的写屏障；即使实验提前失败也能退出。
func (t *transport) Close() error {
	t.mu.Lock()
	if t.gate != nil {
		t.gate.open()
	}
	if t.active != nil {
		t.active.open()
	}
	t.mu.Unlock()
	return t.Conn.Close()
}

// Session 拥有一个 Connection 和其创建的 AMQP channels。AMQP channel 是 TCP
// 上的逻辑会话，不是 Go chan。stop/done 属于取消 watcher；owner 必须 Close 并 join。
type Session struct {
	Conn *amqp.Connection
	Wire *transport
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// Connect 由 scenario 创建，Dial 的 ctx 来自该 scenario 的总预算。
// Recovery:nil 禁用 v1.15.0 可选自动恢复，本课手工建立全新 Connection/Channel，
// 把 topology/Qos/consumer 的重建顺序放在可阅读代码里。
func Connect(ctx context.Context, c Config) (*Session, error) {
	s := &Session{stop: make(chan struct{}), done: make(chan struct{})}
	conn, err := amqp.DialConfig(c.uri(), amqp.Config{
		Heartbeat: 30 * time.Second, Locale: "en_US", Recovery: nil,
		Dial: func(network, address string) (net.Conn, error) {
			n, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			s.Wire = &transport{Conn: n}
			// deadline 给握手/RPC/发送总预算；amqp handshake 可能重置 read deadline，
			// 后面的 watcher 仍会在 ctx 到期时直接关闭本课 socket。
			if deadline, ok := ctx.Deadline(); ok {
				_ = n.SetDeadline(deadline)
			}
			return s.Wire, nil
		},
	})
	if err != nil {
		if s.Wire != nil {
			_ = s.Wire.Close()
		}
		return nil, fmt.Errorf("RabbitMQ 拨号/握手失败（URI已隐藏）: %T", err)
	}
	s.Conn = conn
	go func() {
		defer close(s.done)
		select {
		case <-ctx.Done():
			_ = s.Wire.Close() // cancel 是通知；关闭 socket 才解除不感知 ctx 的 RPC/I/O。
		case <-s.stop:
		}
	}()
	return s, nil
}

// Close 由 scenario 的 defer 调用。使用独立于已失败 ctx 的 3s 清理预算，
// 合并 close/join 错误给 caller，避免主错误遮住资源未退出事实。
func (s *Session) Close() error {
	var result error
	s.once.Do(func() {
		close(s.stop)
		if !s.Conn.IsClosed() {
			result = s.Conn.CloseDeadline(time.Now().Add(3 * time.Second))
		}
		_ = s.Wire.Close()
		joinCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		select {
		case <-s.done:
		case <-joinCtx.Done():
			result = errors.Join(result, errors.New("取消 watcher 未在独立预算内 join"))
		}
	})
	return result
}

// Topology 的每个名字含 m28 + 本次 run + scenario，发布者/恢复者只声明同一对象。
// 串行实验结束就删除本组；绝不扫描或 purge 他人队列。
type Topology struct{ WorkX, DeadX, RetryX, WorkQ, DeadQ, RetryQ string }

func Names(prefix string) Topology {
	return Topology{prefix + ".work.x", prefix + ".dead.x", prefix + ".retry.x", prefix + ".work.q", prefix + ".dead.q", prefix + ".retry.q"}
}

// Declare 由发布者初始化和消费者恢复时调用。durable 保留拓扑；persistent
// 是消息属性，二者不是同一个开关。classic 队列便于单节点有界教学，非复制集承诺。
func (t Topology) Declare(ch *amqp.Channel) error {
	for _, x := range []string{t.WorkX, t.DeadX, t.RetryX} {
		// direct=精确routing key；durable=true，autoDelete/internal/noWait=false。
		// noWait=false意味着声明返回前收到broker declare-ok，参数冲突返回协议错误。
		if err := ch.ExchangeDeclare(x, "direct", true, false, false, false, nil); err != nil {
			return err
		}
	}
	// amqp.Table是map[string]any：动态值必须符合AMQP字段表类型，这里显式int32。
	// work的最终Nack(false,false)进入dead；retry的TTL到期回到work。
	work := amqp.Table{
		"x-queue-type": "classic", "x-max-length": int32(32), "x-overflow": "reject-publish",
		"x-dead-letter-exchange": t.DeadX, "x-dead-letter-routing-key": "dead",
	}
	retry := amqp.Table{
		"x-queue-type": "classic", "x-max-length": int32(32), "x-overflow": "reject-publish",
		"x-message-ttl": int32(150), "x-dead-letter-exchange": t.WorkX, "x-dead-letter-routing-key": "work",
	}
	for _, q := range []struct {
		name, key, x string
		args         amqp.Table
	}{{t.WorkQ, "work", t.WorkX, work}, {t.DeadQ, "dead", t.DeadX, amqp.Table{"x-queue-type": "classic", "x-max-length": int32(32)}}, {t.RetryQ, "retry", t.RetryX, retry}} {
		// queue durable=true，autoDelete/exclusive/noWait=false：消费连接退出时
		// queue仍存在，未ack消息才有地方重投；所有对象最后由本run显式删除。
		if _, err := ch.QueueDeclare(q.name, true, false, false, false, q.args); err != nil {
			return err
		}
		if err := ch.QueueBind(q.name, q.key, q.x, false, nil); err != nil {
			return err
		}
	}
	return nil
}

// Delete 只由拥有此 run 的 owner 在消费者连接已 Close/join 后调用。
// 先删除队列再exchange；每个对象临时创建/关闭一个channel，避免部分Declare
// 失败后某个不存在对象的404把同channel后续删除全部拖成ErrClosed。
// 只对“本run对象已经不存在”的404视为清理完成，其他错误合并返回caller。
func (t Topology) Delete(s *Session) error {
	var result error
	objects := []struct {
		name  string
		queue bool
	}{{t.WorkQ, true}, {t.DeadQ, true}, {t.RetryQ, true}, {t.WorkX, false}, {t.DeadX, false}, {t.RetryX, false}}
	for _, object := range objects {
		ch, err := s.Conn.Channel()
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		if object.queue {
			_, err = ch.QueueDelete(object.name, false, false, false)
		} else {
			err = ch.ExchangeDelete(object.name, false, false)
		}
		var protocol *amqp.Error
		// errors.As按错误链提取协议错误；不按模糊字符串吞掉真正的网络/权限失败。
		if errors.As(err, &protocol) && protocol.Code == 404 {
			err = nil
		}
		result = errors.Join(result, err, ch.Close())
	}
	return result
}

// Publisher 拥有一个 confirm channel 与 buffered return/close 通知。
// 本课串行发布，最多一条在途，所以一个返回可明确归属到 MessageId；生产并发需关联。
type Publisher struct {
	Ch      *amqp.Channel
	Returns <-chan amqp.Return
	Closed  <-chan *amqp.Error
}

// NewPublisher 由execute在连接成功后调用。通知注册早于第一次Publish，
// channel的生命归Session；创建失败由execute的defer关闭整个Session回收部分资源。
func NewPublisher(s *Session) (*Publisher, error) {
	ch, err := s.Conn.Channel()
	if err != nil {
		return nil, err
	}
	p := &Publisher{Ch: ch, Returns: ch.NotifyReturn(make(chan amqp.Return, 4)), Closed: ch.NotifyClose(make(chan *amqp.Error, 1))}
	if err = ch.Confirm(false); err != nil {
		return nil, err
	}
	return p, nil
}

// Publish 由 scenario/retry worker 调用。返回 nil 只在这一次 publish 得到 broker
// ACK confirm 后发生；mandatory 的 return 由 caller 另读，业务账本由 consumer 另写。
func (p *Publisher) Publish(ctx context.Context, x, key, id string, headers amqp.Table) error {
	dc, err := p.Ch.PublishWithDeferredConfirmWithContext(ctx, x, key, true, false, amqp.Publishing{
		MessageId: id, DeliveryMode: amqp.Persistent, ContentType: "text/plain", Body: []byte("coupon:" + id), Headers: headers,
	})
	if err != nil {
		return err
	}
	if dc == nil {
		return errors.New("confirm 模式缺失，不能证明 broker 接受")
	}
	ack, err := dc.WaitContext(ctx) // 当前 goroutine 是 confirm 等待 owner；取消不撤回 broker 副作用。
	if err != nil {
		return fmt.Errorf("等待 confirm 结束但发布结果仍可能未知: %w", err)
	}
	if !ack {
		return errors.New("broker publisher nack")
	}
	return nil
}

// Consumer 保存 consumer tag 和三种等待来源。Delivery.Acknowledger 指向接收它的
// 原 AMQP channel；不要把 DeliveryTag uint64 拿到新 channel 当作可迁移业务ID。
type Consumer struct {
	Ch         *amqp.Channel
	Deliveries <-chan amqp.Delivery
	Closed     <-chan *amqp.Error
	Canceled   <-chan string
	Tag        string
}

// NewConsumer 由scenario.consumer调用。tag是本run的consumer身份，不是delivery
// tag；重连需要重新建立三个exchange/queue、Qos和Consume，函数没有后台业务worker。
func NewConsumer(s *Session, t Topology, tag string, prefetch int) (*Consumer, error) {
	ch, err := s.Conn.Channel()
	if err != nil {
		return nil, err
	}
	if err = t.Declare(ch); err != nil {
		return nil, err
	}
	if err = ch.Qos(prefetch, 0, false); err != nil {
		return nil, err
	}
	c := &Consumer{Ch: ch, Closed: ch.NotifyClose(make(chan *amqp.Error, 1)), Canceled: ch.NotifyCancel(make(chan string, 1)), Tag: tag}
	// Consume后第一项false是autoAck=false；后面exclusive/noLocal/noWait也为false。
	// 返回的是只读Go delivery channel，数据由真实broker经过库解析写入；本课只接收，
	// 不close这个channel，Connection/channel关闭时库负责终止它。
	c.Deliveries, err = ch.Consume(t.WorkQ, tag, false, false, false, false, nil)
	return c, err
}

// Next 由 consumer owner 同步调用；select 同时观察数据、broker 取消、channel
// 异常和总预算，避免消费者提前消失而 owner 永远等待一条不会到来的消息。
func (c *Consumer) Next(ctx context.Context) (amqp.Delivery, error) {
	select {
	case d, ok := <-c.Deliveries:
		if !ok {
			return d, errors.New("delivery stream 已关闭")
		}
		return d, nil
	case e, ok := <-c.Closed:
		if !ok {
			return amqp.Delivery{}, errors.New("AMQP channel 正常关闭")
		}
		return amqp.Delivery{}, fmt.Errorf("AMQP channel 异常: %w", e)
	case tag := <-c.Canceled:
		return amqp.Delivery{}, fmt.Errorf("broker 取消 consumer=%s", tag)
	case <-ctx.Done():
		return amqp.Delivery{}, ctx.Err()
	}
}

// Barrier 发送一个同步 QueueInspect，等待同 channel 的 declare-ok 返回。
// 前面的异步 Ack 已在同 channel 先送达 broker，因而不把 Ack(nil) 当“broker已处理”。
// Messages 只表示 ready 数，不能用 ready=0 推断 unacked=0；反例还会关连接后再查。
func Barrier(ch *amqp.Channel, q string) (amqp.Queue, error) { return ch.QueueInspect(q) }
