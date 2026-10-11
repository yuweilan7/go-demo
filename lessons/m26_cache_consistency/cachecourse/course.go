package cachecourse

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// lab把这次运行的真实池、独占表、精确键和输出组合起来。各实验借用lab，不拥有
// 数据清理权；Run是最终owner。check失败立即返回，已经登记的worker仍由各实验defer加入。
type lab struct {
	db     *sql.DB
	c      cache
	table  string
	out    io.Writer
	config config
}

// check由实验主goroutine同步调用，把不变量和实际值一起输出；details不包含Config/秘密。
func (l *lab) check(ok bool, label string, details any) error {
	if !ok {
		return fmt.Errorf("断言失败 %s：%v", label, details)
	}
	fmt.Fprintf(l.out, "[PASS %s] %v\n", label, details)
	return nil
}

// Run由demo和显式integration测试调用。随机namespace碰撞前先查表/键，所有数据有界。
// 业务ctx已失败时清理重新建8秒Background预算；DROP/DEL失败、Close失败与原失败Join。
func Run(ctx context.Context, out io.Writer) (err error) {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	db, client, err := cfg.clients()
	if err != nil {
		return err
	}
	var ownedTable bool
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		return errors.Join(err, db.Close(), client.Close())
	}
	run := hex.EncodeToString(random)
	table := "m26_" + run
	keys := &registry{keys: make(map[string]bool), prefix: "golearn:m26:" + run + ":"}
	l := &lab{db: db, c: cache{client: client, keys: keys}, table: table, out: out, config: cfg}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		list := keys.list()
		sort.Strings(list)
		// 所有实验已经先join；此处DEL后不会有晚到的回填。逐名EXISTS再次验证真实不存在。
		if len(list) > 0 {
			err = errors.Join(err, client.Del(cleanup, list...).Err())
			remaining, e := client.Exists(cleanup, list...).Result()
			err = errors.Join(err, e)
			if remaining != 0 {
				err = errors.Join(err, fmt.Errorf("Redis清理剩余%d键", remaining))
			}
			fmt.Fprintf(out, "[cleanup.redis] keys=%d remaining=%d exact=%v\n", len(list), remaining, list)
		}
		if ownedTable {
			_, e := db.ExecContext(cleanup, "DROP TABLE `"+table+"`")
			err = errors.Join(err, e)
			var remaining int
			e = db.QueryRowContext(cleanup, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&remaining)
			err = errors.Join(err, e)
			if remaining != 0 {
				err = errors.Join(err, errors.New("表清理后仍存在"))
			}
			fmt.Fprintf(out, "[cleanup.mysql] table=%s remaining=%d\n", table, remaining)
		}
		redisCloseErr, sqlCloseErr := client.Close(), db.Close()
		err = errors.Join(err, redisCloseErr, sqlCloseErr)
		st := db.Stats()
		fmt.Fprintf(out, "[cleanup.pools] mysql_open=%d inuse=%d idle=%d redis_close_ok=%v\n", st.OpenConnections, st.InUse, st.Idle, redisCloseErr == nil)
		if st.OpenConnections != 0 || st.InUse != 0 {
			err = errors.Join(err, errors.New("SQL池未归零"))
		}
	}()
	var version, database string
	if err = db.QueryRowContext(ctx, "SELECT VERSION(),DATABASE()").Scan(&version, &database); err != nil {
		return err
	}
	info, e := client.Info(ctx, "server").Result()
	if e != nil {
		return e
	}
	if err = l.check(version == cfg.mysqlVersion && database == "golearn_m20" && strings.Contains(info, "redis_version:"+cfg.redisVersion), "versions", fmt.Sprintf("mysql=%s redis=%s database=%s", version, cfg.redisVersion, database)); err != nil {
		return err
	}
	var occupied int
	if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&occupied); err != nil {
		return err
	}
	if occupied != 0 {
		return errors.New("随机表名已占用，拒绝CREATE/DROP")
	}
	ownedTable = true // 已证明此前不存在，CREATE响应丢失也必须检查/精确清理自己这个目标。
	if _, err = db.ExecContext(ctx, "CREATE TABLE `"+table+"` (tenant VARCHAR(64) NOT NULL,user_id VARCHAR(64) NOT NULL,version INT NOT NULL,name TEXT NOT NULL,PRIMARY KEY(tenant,user_id)) ENGINE=InnoDB"); err != nil {
		return err
	}
	for _, r := range []struct{ t, u, n string }{{"A", "u1", "A-user1"}, {"B", "u1", "B-user1"}, {"A", "u2", "A-user2"}} {
		if _, err = db.ExecContext(ctx, "INSERT INTO `"+table+"` VALUES (?,?,1,?)", r.t, r.u, r.n); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "[namespace] table=%s prefix=%s MaxOpen=4 source_capacity=2\n", table, keys.prefix)
	for _, experiment := range []func(context.Context) error{l.scopeAndRollback, l.staleRefill, l.flightCases, l.policies} {
		if err = experiment(ctx); err != nil {
			return err
		}
	}
	return nil
}

// emptyKey是首次触键检查：先登记再EXISTS，随机run下任何占用都是拒绝，不覆盖他人键。
// scene内复用已登记键的操作不再调用本函数；实际业务key由registry.key统一生成。
func (l *lab) emptyKey(ctx context.Context, key string) error {
	l.c.keys.register(key)
	n, err := l.c.client.Exists(ctx, key).Result()
	if err != nil {
		// preflight没有写操作，读结果未知时也撤登记；清理不能把未确认的已有键当成自己的。
		l.c.keys.mu.Lock()
		delete(l.c.keys.keys, key)
		l.c.keys.mu.Unlock()
		return err
	}
	if n != 0 {
		// 占用说明这个键不属于本run，撤登记使最终清理不会删除它。
		l.c.keys.mu.Lock()
		delete(l.c.keys.keys, key)
		l.c.keys.mu.Unlock()
		return errors.New("随机Redis键已占用，拒绝覆盖")
	}
	return nil
}

// scopeAndRollback是E01，caller是Run。危险键真实缓存A/u1后，B/u1、A/u2、schema2
// 分别命中错误对象；修复键让每个scope都回自己的源。rollback分支独立SELECT/GET/计数
// 证明“没有提交，也没有成功失效”，不凭ErrRollback文案推测数据库结局。
func (l *lab) scopeAndRollback(ctx context.Context) error {
	fmt.Fprintln(l.out, "\nE01：维度、cache-aside、回滚")
	s := newSource(l.db, l.table)
	a := scope{"A", "u1", 1}
	cases := []struct {
		scene string
		other scope
		wrong bool
	}{{"omit-tenant", scope{"B", "u1", 1}, true}, {"omit-user", scope{"A", "u2", 1}, true}, {"omit-schema", scope{"A", "u1", 2}, true}}
	for _, v := range cases {
		key := l.c.keys.register(l.c.keys.prefix + v.scene + ":bad")
		if err := l.emptyKey(ctx, key); err != nil {
			return err
		}
		if _, err := l.c.aside(ctx, s, a, key, nil, nil); err != nil {
			return err
		}
		bad, err := l.c.aside(ctx, s, v.other, key, nil, nil)
		if err != nil {
			return err
		}
		if err = l.check(bad.Tenant != v.other.Tenant || bad.User != v.other.User || bad.Schema != v.other.Schema, v.scene+".wrong-result", bad); err != nil {
			return err
		}
	}
	for _, id := range []scope{a, {"B", "u1", 1}, {"A", "u2", 1}, {"A", "u1", 2}} {
		key := l.c.keys.key("safe", id, "")
		if err := l.emptyKey(ctx, key); err != nil {
			return err
		}
		d, err := l.c.aside(ctx, s, id, key, nil, nil)
		if err != nil {
			return err
		}
		if err = l.check(d.Tenant == id.Tenant && d.User == id.User && d.Schema == id.Schema, "scope-isolated", d); err != nil {
			return err
		}
	}
	key := l.c.keys.key("safe", a, "")
	before := s.snapshot().Queries
	if _, err := l.c.aside(ctx, s, a, key, nil, nil); err != nil {
		return err
	}
	if err := l.check(s.snapshot().Queries == before, "cache-hit-no-source", before); err != nil {
		return err
	}
	published := 0
	committed, err := s.update(ctx, a, "MUST-ROLLBACK", true, func() error { published++; return l.c.client.Del(ctx, key).Err() })
	if !errors.Is(err, ErrRollback) {
		return fmt.Errorf("回滚错误链不匹配：%w", err)
	}
	sqlRow, e := s.Read(ctx, a, nil, nil)
	if e != nil {
		return e
	}
	cached, hit, e := l.c.get(ctx, key)
	if e != nil {
		return e
	}
	return l.check(!committed && published == 0 && sqlRow.Version == 1 && sqlRow.Name == "A-user1" && hit && cached.Name == sqlRow.Name, "rollback-no-invalidation", fmt.Sprintf("committed=%v published=%d mysql=%v cache=%v", committed, published, sqlRow, cached))
}

// staleRefill是核心E02，同一A/u1从v1推进到v5。owner负责所有旧读任务、gate、join；
// 每次oldReadDone都发生在真正QueryRow.Scan之后。writer.Commit/DEL完成才放回填门。
// 危险双删、版本键、代际Lua、提交失效窗口用同一业务实体对照，均为真实SQL/Redis。
func (l *lab) staleRefill(ctx context.Context) (err error) {
	fmt.Fprintln(l.out, "\nE02：旧读 → 新写commit/delete → 旧回填")
	id := scope{"A", "u1", 1}
	s := newSource(l.db, l.table)
	o := newOwner(ctx)
	defer o.finish(&err)
	seen, hold := newGate(), newGate()
	o.registerGate(hold)
	key := l.c.keys.key("dirty", id, "")
	if err = l.emptyKey(ctx, key); err != nil {
		return err
	}
	result := make(chan DTO, 1)
	old := o.start(func(c context.Context) error {
		d, e := l.c.aside(c, s, id, key, seen, hold)
		if e == nil {
			result <- d
		}
		return e
	})
	if err = await(ctx, seen.c, "旧SELECT完成"); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[timeline.old] old SQL=v1；reader等待回填门")
	committed, e := s.update(ctx, id, "new-v2", false, func() error { return l.c.client.Del(ctx, key).Err() })
	if e != nil {
		return e
	}
	// 第二次删也在旧reader放行之前；它不能禁止更晚的旧SET，证明双删依赖时序假设。
	if err = l.c.client.Del(ctx, key).Err(); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[timeline.writer] COMMIT=v2 → DEL完成 → 第二次DEL完成")
	hold.open()
	if err = await(ctx, old.done, "旧回填Done"); err != nil {
		return err
	}
	if old.err != nil {
		return old.err
	}
	d := <-result
	cached, hit, e := l.c.get(ctx, key)
	if e != nil {
		return e
	}
	sqlVersion, e := s.currentVersion(ctx, id)
	if e != nil {
		return e
	}
	if err = l.check(committed && d.Version == 1 && hit && cached.Version == 1 && sqlVersion == 2, "dirty-refill-and-double-delete", fmt.Sprintf("old=%d cache=%d mysql=%d source=%+v", d.Version, cached.Version, sqlVersion, s.snapshot())); err != nil {
		return err
	}
	if err = l.c.client.PExpire(ctx, key, 300*time.Millisecond).Err(); err != nil {
		return err
	}
	// ticker只限制轮询频率，结束条件始终是实际GET=redis.Nil，不依据时间猜缓存已经过期。
	expiry, cancel := context.WithTimeout(ctx, 3*time.Second)
	err = waitMissing(expiry, l.c.client, key)
	cancel()
	if err != nil {
		return err
	}
	if err = l.check(true, "ttl-bounds-residence", "真实GET已miss；TTL没有避免刚才读到v1"); err != nil {
		return err
	}

	// 版本键reader把旧版本数据只写v2地址；当前reader先向DB拿v3，旧v2仍在但不选它。
	seen2, hold2 := newGate(), newGate()
	o.registerGate(hold2)
	oldKey := l.c.keys.key("versioned", id, ":v=2")
	if err = l.emptyKey(ctx, oldKey); err != nil {
		return err
	}
	old2 := o.start(func(c context.Context) error {
		d, e := s.Read(c, id, seen2, hold2)
		if e != nil {
			return e
		}
		return l.c.put(c, oldKey, d, jitter(oldKey))
	})
	if err = await(ctx, seen2.c, "v2 SQL完成"); err != nil {
		return err
	}
	if _, err = s.update(ctx, id, "new-v3", false, nil); err != nil {
		return err
	}
	hold2.open()
	if err = await(ctx, old2.done, "v2版本键填充Done"); err != nil {
		return err
	}
	if old2.err != nil {
		return old2.err
	}
	current, e := s.currentVersion(ctx, id)
	if e != nil {
		return e
	}
	newKey := l.c.keys.key("versioned", id, fmt.Sprintf(":v=%d", current))
	if err = l.emptyKey(ctx, newKey); err != nil {
		return err
	}
	newRow, e := l.c.versioned(ctx, s, id, current, newKey)
	if e != nil {
		return e
	}
	oldRow, oldHit, e := l.c.get(ctx, oldKey)
	if e != nil {
		return e
	}
	if err = l.check(oldHit && oldRow.Version == 2 && current == 3 && newRow.Version == 3, "versioned-address", fmt.Sprintf("old_key=v%d selected=v%d version_query=1", oldRow.Version, newRow.Version)); err != nil {
		return err
	}
	// 实际SQL已v3，模拟caller仍带v2期望：拒绝将v3数据写到v2的空地址，不无界重试。
	mismatchKey := l.c.keys.key("version-mismatch", id, ":v=2")
	if err = l.emptyKey(ctx, mismatchKey); err != nil {
		return err
	}
	_, mismatchErr := l.c.versioned(ctx, s, id, 2, mismatchKey)
	mismatchCount, e := l.c.client.Exists(ctx, mismatchKey).Result()
	if e != nil {
		return e
	}
	if err = l.check(errors.Is(mismatchErr, ErrVersionChanged) && mismatchCount == 0, "version-lookup-change-rejects-fill", "expected=v2 SQL=v3 EXISTS=0"); err != nil {
		return err
	}

	// fence的代际键在本run90s存活，业务最大70s/每源读取6s；不会在这次读中丢失回到0。
	genKey := l.c.keys.key("fence", id, ":generation")
	dataKey := l.c.keys.key("fence", id, ":data")
	for _, k := range []string{genKey, dataKey} {
		if err = l.emptyKey(ctx, k); err != nil {
			return err
		}
	}
	generation, e := l.c.generation(ctx, genKey)
	if e != nil {
		return e
	}
	seen3, hold3 := newGate(), newGate()
	o.registerGate(hold3)
	filled := make(chan bool, 1)
	old3 := o.start(func(c context.Context) error {
		d, e := s.Read(c, id, seen3, hold3)
		if e != nil {
			return e
		}
		ok, e := l.c.fillFence(c, genKey, dataKey, generation, d)
		filled <- ok
		return e
	})
	if err = await(ctx, seen3.c, "v3 SQL完成"); err != nil {
		return err
	}
	if _, err = s.update(ctx, id, "new-v4", false, func() error { return l.c.client.Eval(ctx, fenceInvalidate, []string{genKey, dataKey}).Err() }); err != nil {
		return err
	}
	hold3.open()
	if err = await(ctx, old3.done, "旧fence填充Done"); err != nil {
		return err
	}
	if old3.err != nil {
		return old3.err
	}
	accepted := <-filled
	_, fenceHit, e := l.c.get(ctx, dataKey)
	if e != nil {
		return e
	}
	if err = l.check(!accepted && !fenceHit, "generation-rejects-old-fill", "reader generation=0；writer generation=1；Lua返回0"); err != nil {
		return err
	}
	fresh, e := s.Read(ctx, id, nil, nil)
	if e != nil {
		return e
	}
	generation, e = l.c.generation(ctx, genKey)
	if e != nil {
		return e
	}
	ok, e := l.c.fillFence(ctx, genKey, dataKey, generation, fresh)
	if e != nil {
		return e
	}
	if err = l.check(ok && fresh.Version == 4, "generation-new-fill", fresh); err != nil {
		return err
	}
	// 故意暂停/遗漏提交后失效：两套服务没有原子边界，此时命中仍旧，fence也不能识别。
	if _, err = s.update(ctx, id, "new-v5", false, nil); err != nil {
		return err
	}
	window, windowHit, e := l.c.get(ctx, dataKey)
	if e != nil {
		return e
	}
	v, e := s.currentVersion(ctx, id)
	if e != nil {
		return e
	}
	if err = l.check(windowHit && window.Version == 4 && v == 5, "commit-invalidation-window", fmt.Sprintf("cache=v%d mysql=v%d", window.Version, v)); err != nil {
		return err
	}
	if err = l.c.client.Eval(ctx, fenceInvalidate, []string{genKey, dataKey}).Err(); err != nil {
		return err
	}
	return nil
}

// waitMissing由TTL实验调用，timer owner是本函数，return时Stop；轮询错误原样交上层。
func waitMissing(ctx context.Context, c *redis.Client, key string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := c.Get(ctx, key).Result()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// flightCases把SF三个容易混淆的契约放在真实SELECT上：leader ctx污染、follower取消
// 不终止服务ctx工作、返回DTO的map/slice仍共享。所有startFlight同步登记后才开门，
// body只有一次回源；Result发布和wrapper Done用cleanup门进一步分开。
func (l *lab) flightCases(ctx context.Context) (err error) {
	fmt.Fprintln(l.out, "\nE03：singleflight key、ctx、共享结果与Done")
	id := scope{"A", "u1", 1}
	// SF key也必须覆盖授权/表示维度：第二个callback会被忽略，不会因为它捕获B就执行。
	{
		o := newOwner(ctx)
		defer o.finish(&err)
		seen, hold := newGate(), newGate()
		o.registerGate(hold)
		s := newSource(l.db, l.table)
		var group singleflight.Group
		a := startFlight(o, &group, "BAD-only-user-u1", o.ctx, true, nil, func(c context.Context) (DTO, error) { return s.Read(c, id, seen, hold) })
		if err = await(ctx, seen.c, "SF A租户SQL"); err != nil {
			return err
		}
		b := startFlight(o, &group, "BAD-only-user-u1", o.ctx, true, nil, func(c context.Context) (DTO, error) { return s.Read(c, scope{"B", "u1", 1}, nil, nil) })
		hold.open()
		if _, err = a.wait(ctx); err != nil {
			return err
		}
		rb, e := b.wait(ctx)
		if e != nil {
			return e
		}
		if err = l.check(rb.Row.Tenant == "A" && s.snapshot().Queries == 1, "singleflight-key-leaks-tenant", rb.Row); err != nil {
			return err
		}
		// 修复用统一registry.key维度，两个不同key不合并，各做自己的真实SELECT。
		ka := l.c.keys.key("sf-scope", id, "")
		kb := l.c.keys.key("sf-scope", scope{"B", "u1", 1}, "")
		for _, k := range []string{ka, kb} {
			if err = l.emptyKey(ctx, k); err != nil {
				return err
			}
		}
		ta := startFlight(o, &group, ka, o.ctx, true, nil, func(c context.Context) (DTO, error) { return s.Read(c, id, nil, nil) })
		tb := startFlight(o, &group, kb, o.ctx, true, nil, func(c context.Context) (DTO, error) { return s.Read(c, scope{"B", "u1", 1}, nil, nil) })
		ra, e := ta.wait(ctx)
		if e != nil {
			return e
		}
		rb, e = tb.wait(ctx)
		if e != nil {
			return e
		}
		if err = l.check(ra.Row.Tenant == "A" && rb.Row.Tenant == "B" && s.snapshot().Queries == 3, "singleflight-key-scope-repair", s.snapshot()); err != nil {
			return err
		}
	}
	// 坏ctx：第一个请求的ctx成为共享工作ctx，取消leader会使仍有效的follower也得到取消。
	{
		o := newOwner(ctx)
		leaderCtx, leaderCancel := context.WithCancel(ctx)
		seen, hold := newGate(), newGate()
		o.registerGate(hold)
		s := newSource(l.db, l.table)
		var group singleflight.Group
		fn := func(c context.Context) (DTO, error) { return s.Read(c, id, seen, hold) }
		a := startFlight(o, &group, "bad-leader-context", leaderCtx, false, nil, fn)
		if err = await(ctx, seen.c, "leader真实SQL完成"); err != nil {
			leaderCancel()
			o.finish(&err)
			return err
		}
		b := startFlight(o, &group, "bad-leader-context", ctx, false, nil, fn)
		leaderCancel() // hold保持未打开，因此body只能从取消分支退出，不能偶然走成功分支。
		ra, ea := a.wait(ctx)
		rb, eb := b.wait(ctx)
		err = l.check(errors.Is(ea, context.Canceled) && errors.Is(eb, context.Canceled) && ra.Shared && rb.Shared && s.snapshot().Queries == 1, "leader-context-poisons-followers", s.snapshot())
		o.finish(&err)
		if err != nil {
			return err
		}
	}
	// 好ctx：整个flight由服务owner ctx+源库预算限制，各请求只取消自己的等待。
	{
		o := newOwner(ctx)
		defer o.finish(&err)
		seen, hold, cleanup := newGate(), newGate(), newGate()
		o.registerGate(hold)
		o.registerGate(cleanup)
		s := newSource(l.db, l.table)
		var group singleflight.Group
		key := l.c.keys.key("singleflight", id, "")
		if err = l.emptyKey(ctx, key); err != nil {
			return err
		}
		fn := func(c context.Context) (DTO, error) {
			d, e := s.Read(c, id, seen, hold)
			if e != nil {
				return DTO{}, e
			}
			return d, l.c.put(c, key, d, jitter(key))
		}
		a := startFlight(o, &group, key, o.ctx, false, cleanup, fn)
		if err = await(ctx, seen.c, "service leader SQL完成"); err != nil {
			return err
		}
		b := startFlight(o, &group, key, o.ctx, false, cleanup, fn)
		canceled := startFlight(o, &group, key, o.ctx, false, cleanup, fn)
		waitCtx, waitCancel := context.WithCancel(ctx)
		waitCancel()
		_, cancelErr := canceled.wait(waitCtx)
		select {
		case <-a.done:
			return errors.New("follower取消时leader已错误完成")
		default:
		}
		if err = l.check(errors.Is(cancelErr, context.Canceled) && s.snapshot().Active == 1, "follower-cancel-is-not-leader-done", s.snapshot()); err != nil {
			return err
		}
		hold.open()
		ra, e := a.wait(ctx)
		if e != nil {
			return e
		}
		rb, e := b.wait(ctx)
		if e != nil {
			return e
		}
		// 第三个caller已离开，但drainer仍消费其库Result；无资源卡在无人接收的无缓冲send。
		_, e = canceled.wait(ctx)
		if e != nil {
			return e
		}
		cached, hit, e := l.c.get(ctx, key)
		if e != nil {
			return e
		}
		select {
		case <-a.done:
			return errors.New("Result被误当作最终Done")
		default:
		}
		if err = l.check(hit && cached.Version == 5 && s.snapshot().Queries == 1 && ra.Shared && rb.Shared, "one-source-result-side-effect-before-done", fmt.Sprintf("source=%+v cache=v%d drainer_done=false", s.snapshot(), cached.Version)); err != nil {
			return err
		}
		ra.Row.Labels["source"] = "caller-A"
		ra.Row.Tags[0] = "changed-A"
		if err = l.check(rb.Row.Labels["source"] == "caller-A" && rb.Row.Tags[0] == "changed-A", "shared-object-pollution", rb.Row); err != nil {
			return err
		}
		cleanup.open()
		if err = o.join(ctx); err != nil {
			return err
		}
	}
	// 同样的共享库DTO在每个drainer返回caller前Clone，修改第一份不改变第二份。
	{
		o := newOwner(ctx)
		defer o.finish(&err)
		seen, hold := newGate(), newGate()
		o.registerGate(hold)
		s := newSource(l.db, l.table)
		var group singleflight.Group
		fn := func(c context.Context) (DTO, error) { return s.Read(c, id, seen, hold) }
		a := startFlight(o, &group, "clone", o.ctx, true, nil, fn)
		if err = await(ctx, seen.c, "clone SQL完成"); err != nil {
			return err
		}
		b := startFlight(o, &group, "clone", o.ctx, true, nil, fn)
		hold.open()
		ra, e := a.wait(ctx)
		if e != nil {
			return e
		}
		rb, e := b.wait(ctx)
		if e != nil {
			return e
		}
		ra.Row.Labels["source"] = "caller-A"
		ra.Row.Tags[0] = "changed-A"
		if err = l.check(rb.Row.Labels["source"] == "mysql" && rb.Row.Tags[0] == "original" && s.snapshot().Queries == 1, "clone-isolates-callers", rb.Row); err != nil {
			return err
		}
	}
	// 两个Group表示两份进程内命名空间。不是跨进程实测，但真实2次SQL证明库无全局注册。
	{
		o := newOwner(ctx)
		defer o.finish(&err)
		seen1, seen2, hold := newGate(), newGate(), newGate()
		o.registerGate(hold)
		s := newSource(l.db, l.table)
		var g1, g2 singleflight.Group
		a := startFlight(o, &g1, "same-key", o.ctx, true, nil, func(c context.Context) (DTO, error) { return s.Read(c, id, seen1, hold) })
		b := startFlight(o, &g2, "same-key", o.ctx, true, nil, func(c context.Context) (DTO, error) { return s.Read(c, id, seen2, hold) })
		if err = await(ctx, seen1.c, "group1 SQL"); err != nil {
			return err
		}
		if err = await(ctx, seen2.c, "group2 SQL"); err != nil {
			return err
		}
		hold.open()
		if _, err = a.wait(ctx); err != nil {
			return err
		}
		if _, err = b.wait(ctx); err != nil {
			return err
		}
		if err = l.check(s.snapshot().Queries == 2 && s.snapshot().Peak == 2, "independent-groups-duplicate-source", s.snapshot()); err != nil {
			return err
		}
	}
	return nil
}

// policies是E04：负缓存延迟新建可见性、真实不同PTTL、大DTO拒写、热点真实过期后
// SF合并、AUTH故障的有限回源。不是压测或Redis停机证明，所有策略参数是合成示例。
func (l *lab) policies(ctx context.Context) (err error) {
	fmt.Fprintln(l.out, "\nE04：负缓存/jitter/热点/大key/不可用预算")
	s := newSource(l.db, l.table)
	id := scope{"A", "missing", 1}
	key := l.c.keys.key("negative", id, "")
	if err = l.emptyKey(ctx, key); err != nil {
		return err
	}
	d, e := l.c.aside(ctx, s, id, key, nil, nil)
	if e != nil {
		return e
	}
	_, e = l.c.aside(ctx, s, id, key, nil, nil)
	if e != nil {
		return e
	}
	if _, e = l.db.ExecContext(ctx, "INSERT INTO `"+l.table+"` VALUES ('A','missing',1,'created-after-negative')"); e != nil {
		return e
	}
	stale, e := l.c.aside(ctx, s, id, key, nil, nil)
	if e != nil {
		return e
	}
	if err = l.check(!d.Found && !stale.Found && s.snapshot().Queries == 1, "negative-cache-creation-window", s.snapshot()); err != nil {
		return err
	}
	if err = l.c.client.Del(ctx, key).Err(); err != nil {
		return err
	}
	fresh, e := l.c.aside(ctx, s, id, key, nil, nil)
	if e != nil {
		return e
	}
	if err = l.check(fresh.Found && fresh.Name == "created-after-negative" && s.snapshot().Queries == 2, "negative-invalidate-repair", fresh); err != nil {
		return err
	}
	ttls := map[int64]bool{}
	for i := 0; i < 12; i++ {
		k := l.c.keys.key("jitter", scope{"A", fmt.Sprintf("j%d", i), 1}, "")
		if err = l.emptyKey(ctx, k); err != nil {
			return err
		}
		ttl := jitter(k)
		ttls[ttl.Milliseconds()] = true
		if err = l.c.put(ctx, k, fresh, ttl); err != nil {
			return err
		}
		pttl, e := l.c.client.PTTL(ctx, k).Result()
		if e != nil {
			return e
		}
		if err = l.check(pttl > 0 && pttl <= ttl, "jitter-real-pttl", fmt.Sprintf("configured=%s actual=%s", ttl, pttl)); err != nil {
			return err
		}
	}
	if err = l.check(len(ttls) > 1, "jitter-distinct-expiries", ttls); err != nil {
		return err
	}
	bigID := scope{"A", "large", 1}
	if _, err = l.db.ExecContext(ctx, "INSERT INTO `"+l.table+"` VALUES (?,?,1,?)", bigID.Tenant, bigID.User, strings.Repeat("x", 5000)); err != nil {
		return err
	}
	bigKey := l.c.keys.key("large", bigID, "")
	if err = l.emptyKey(ctx, bigKey); err != nil {
		return err
	}
	big, e := l.c.aside(ctx, s, bigID, bigKey, nil, nil)
	if e != nil {
		return e
	}
	putErr := l.c.put(ctx, bigKey, big, 30*time.Second)
	n, e := l.c.client.Exists(ctx, bigKey).Result()
	if e != nil {
		return e
	}
	if err = l.check(errors.Is(putErr, ErrLargeDTO) && n == 0 && big.Found && len(big.Name) == 5000, "large-dto-bypass", "真实SQL返回5000字节name；业务返回完整数据；4KiB策略拒写缓存"); err != nil {
		return err
	}
	// 热点过期由GET=nil确认，随后6个同key调用在source门后同步登记，只有1个SQL。
	hotKey := l.c.keys.key("hot", scope{"A", "u1", 1}, "")
	if err = l.emptyKey(ctx, hotKey); err != nil {
		return err
	}
	if err = l.c.put(ctx, hotKey, fresh, 100*time.Millisecond); err != nil {
		return err
	}
	if err = waitMissing(ctx, l.c.client, hotKey); err != nil {
		return err
	}
	{
		o := newOwner(ctx)
		defer o.finish(&err)
		src := newSource(l.db, l.table)
		seen, hold := newGate(), newGate()
		o.registerGate(hold)
		var group singleflight.Group
		fn := func(c context.Context) (DTO, error) {
			return l.c.aside(c, src, scope{"A", "u1", 1}, hotKey, seen, hold)
		}
		tickets := []ticket{startFlight(o, &group, hotKey, o.ctx, true, nil, fn)}
		if err = await(ctx, seen.c, "热点source SQL"); err != nil {
			return err
		}
		for i := 0; i < 5; i++ {
			tickets = append(tickets, startFlight(o, &group, hotKey, o.ctx, true, nil, fn))
		}
		hold.open()
		for _, t := range tickets {
			r, e := t.wait(ctx)
			if e != nil {
				return e
			}
			if !r.Shared {
				return errors.New("热点结果应共享")
			}
		}
		if err = l.check(src.snapshot().Queries == 1 && src.snapshot().Peak == 1, "hot-expiry-coalesced-six", src.snapshot()); err != nil {
			return err
		}
	}
	return l.unavailable(ctx)
}

// unavailable构造独占WRONGPASS客户端，正常Redis仍可PING。所有请求真实GET失败后才
// 进入显式允许的profile-read降级；两项占source容量，后六项立即拒绝。原AUTH与Overload
// 错误同时保留，绝不“缓存错就无限查DB”；这里没有订单写入降级为Redis真相的分支。
func (l *lab) unavailable(ctx context.Context) (err error) {
	bad := l.config.redisClient(true)
	defer func() { err = errors.Join(err, bad.Close()) }()
	bc := cache{client: bad, keys: l.c.keys}
	id := scope{"A", "u1", 1}
	key := l.c.keys.key("unavailable", id, "")
	if err = l.emptyKey(ctx, key); err != nil {
		return err
	}
	_, _, authErr := bc.get(ctx, key)
	if authErr == nil || !redis.HasErrorPrefix(rootError(authErr), "WRONGPASS") {
		return fmt.Errorf("故障必须来自真实AUTH拒绝：%w", authErr)
	}
	o := newOwner(ctx)
	defer o.finish(&err)
	s := newSource(l.db, l.table)
	hold := newGate()
	o.registerGate(hold)
	seen1, seen2 := newGate(), newGate()
	fallback := func(c context.Context, seen *gate) (DTO, error) {
		_, _, cacheErr := bc.get(c, key)
		if cacheErr == nil {
			return DTO{}, errors.New("故障GET意外成功")
		}
		d, e := s.Read(c, id, seen, hold)
		if e != nil {
			return DTO{}, errors.Join(cacheErr, e)
		}
		return d, nil // 当前业务允许profile读从SQL返回；成功降级不把cacheErr当数据库失败。
	}
	results := make(chan DTO, 2)
	for _, seen := range []*gate{seen1, seen2} {
		observed := seen // 显式当前迭代值，闭包只借这一个门，不共用一个可变循环变量。
		o.start(func(c context.Context) error {
			d, e := fallback(c, observed)
			if e == nil {
				results <- d
			}
			return e
		})
	}
	if err = await(ctx, seen1.c, "降级SQL1"); err != nil {
		return err
	}
	if err = await(ctx, seen2.c, "降级SQL2"); err != nil {
		return err
	}
	rejected := 0
	for i := 0; i < 6; i++ {
		_, e := fallback(ctx, nil)
		if !errors.Is(e, ErrOverload) {
			return fmt.Errorf("容量满必须拒绝：%w", e)
		}
		if !redis.HasErrorPrefix(rootError(e), "WRONGPASS") {
			return errors.New("拒绝错误丢失原AUTH")
		}
		rejected++
	}
	if err = l.check(s.snapshot().Queries == 2 && s.snapshot().Active == 2 && s.snapshot().Peak == 2 && s.snapshot().Rejected == 6 && rejected == 6, "cache-unavailable-bounded-source", s.snapshot()); err != nil {
		return err
	}
	hold.open()
	if err = o.join(ctx); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		d := <-results
		if !d.Found || d.Version != 5 {
			return errors.New("真实降级结果错误")
		}
	}
	if err = l.c.client.Ping(ctx).Err(); err != nil {
		return err
	}
	// Commit后真实失效AUTH失败：DB事实仍v6，committed=true+error，不能返回“没写入”。
	committed, e := s.update(ctx, id, "committed-cache-failed-v6", false, func() error { return bad.Del(ctx, key).Err() })
	v, qerr := s.currentVersion(ctx, id)
	if qerr != nil {
		return qerr
	}
	return l.check(committed && e != nil && redis.HasErrorPrefix(e, "WRONGPASS") && v == 6 && s.snapshot().Active == 0, "commit-survives-cache-error", fmt.Sprintf("committed=%v mysql=v%d source=%+v normalRedis=PING-ok", committed, v, s.snapshot()))
}

// rootError仅用于定位Redis服务端错误。errors.Join可含多个子树，递归寻找有WRONGPASS
// 前缀的原原因；业务分类仍用errors.Is匹配Overload，而非错误字符串包含判断。
func rootError(err error) error {
	if redis.HasErrorPrefix(err, "WRONGPASS") {
		return err
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range multi.Unwrap() {
			if r := rootError(e); redis.HasErrorPrefix(r, "WRONGPASS") {
				return r
			}
		}
	}
	if e := errors.Unwrap(err); e != nil {
		return rootError(e)
	}
	return err
}
