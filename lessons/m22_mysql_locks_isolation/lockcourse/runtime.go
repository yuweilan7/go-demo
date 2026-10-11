package lockcourse

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
)

// openPool 由 Run 调用，只有 Run 可以 Close 返回的 DB。这里仅实现本课接入，
// 不复制 M21 的业务工程；沿用其 READY/限定库/不打印秘密/真实 connector 契约。
// JSON 中的 credential_file 是路径；秘密只进入 driver 配置内存，不生成 DSN 日志。
func openPool() (*sql.DB, string, error) {
	path := os.Getenv("GOLEARN_SERVICE_INDEX")
	if path == "" {
		path = "D:/IdeaProject/go-demo/.local/course-orchestrator/infra-services.json"
	}
	var index struct {
		Services struct {
			MySQL struct {
				State, Host string
				Port        int
				Version     string `json:"server_version"`
				File        string `json:"credential_file"`
			} `json:"mysql"`
		} `json:"services"`
	}
	if err := readJSON(path, &index); err != nil {
		return nil, "", err
	}
	s := index.Services.MySQL // 是值副本；不会改共享索引。
	if s.State != "READY" || s.Host != "127.0.0.1" || s.Port < 1 || s.Port > 65535 {
		return nil, "", errors.New("需要 READY 的 loopback MySQL 学习入口")
	}
	var secret struct{ Username, Password, Database string }
	if err := readJSON(s.File, &secret); err != nil {
		return nil, "", err
	}
	if secret.Database != "golearn_m20" || secret.Username == "" || secret.Password == "" {
		return nil, "", errors.New("拒绝不属于限定 golearn_m20 的凭据")
	}
	c := mysql.NewConfig()
	c.User, c.Passwd, c.DBName = secret.Username, secret.Password, secret.Database
	c.Net, c.Addr = "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	c.Timeout, c.ReadTimeout, c.WriteTimeout = 3*time.Second, 10*time.Second, 3*time.Second
	c.Logger = &mysql.NopLogger{}
	// 每个新物理连接初始 RC/2秒；隔离实验会在专属 Conn 上改 session，再恢复。
	// 会话参数不是全局 SET，也不影响其他模块的连接。
	c.Params = map[string]string{"transaction_isolation": "'READ-COMMITTED'", "innodb_lock_wait_timeout": "2"}
	connector, err := mysql.NewConnector(c)
	if err != nil {
		return nil, "", err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(4) // 两业务连接 + 观察连接；留一条恢复余量，不耗尽全服务32连接。
	db.SetMaxIdleConns(1)
	return db, s.Version, nil
}

// readJSON 拥有小文件，调用方传指针由 decoder 填值。具名返回 err 允许 defer
// 在读取失败之外保留 Close 失败；不把文件正文写进错误，尤其不能暴露密码。
func readJSON(path string, dst any) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	if err = d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if !errors.Is(d.Decode(&extra), io.EOF) {
		return errors.New("配置只能包含一个 JSON 对象")
	}
	return nil
}

// session 表示本课显式借到的一条物理会话。id 是 MySQL CONNECTION_ID，给
// PROCESSLIST 定位自己的 SQL；不是 goroutine ID，也不能拿它去 KILL 别人。
type session struct {
	conn *sql.Conn
	id   int64
}

// acquire 由双会话实验/worker 调用。Conn 的所有权交给 caller，BeginTx 会在
// 此 Conn 上创建事务。失败时这里自行 Close；成功后 caller 必须登记 release。
func acquire(ctx context.Context, db *sql.DB, isolation string) (_ *session, err error) {
	c, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			err = errors.Join(err, c.Close())
		}
	}()
	if isolation != "READ-COMMITTED" && isolation != "REPEATABLE-READ" {
		return nil, errors.New("不允许任意拼接隔离级别")
	}
	if _, err = c.ExecContext(ctx, "SET SESSION transaction_isolation='"+isolation+"'"); err != nil {
		return nil, err
	}
	var actual string
	var id int64
	if err = c.QueryRowContext(ctx, "SELECT CONNECTION_ID(),@@transaction_isolation").Scan(&id, &actual); err != nil {
		return nil, err
	}
	if actual != isolation {
		return nil, fmt.Errorf("隔离级别实际=%s，期望=%s", actual, isolation)
	}
	ok = true
	return &session{c, id}, nil
}

// release 由会话 owner 在 Tx 清理之后调用。恢复 session 是为了不把 RR 泄漏
// 给下一个借池连接的业务；新 Background 预算不继承已经取消的业务 ctx。
func (s *session) release() error {
	ctx, cancel := cleanupContext()
	defer cancel()
	_, reset := s.conn.ExecContext(ctx, "SET SESSION transaction_isolation='READ-COMMITTED'")
	return errors.Join(reset, s.conn.Close())
}

// rollback 沿用 M21 已验收的契约。ErrTxDone 是本地 Tx 已结束；不把它解释为
// 服务端一定没提交。其他错误保留给 caller，避免清理错误覆盖业务错误。
func rollback(tx *sql.Tx) error {
	if tx == nil {
		return nil
	}
	err := tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

var ErrUnknown = errors.New("COMMIT 结果未知；禁止直接重放")
var ErrNoStock = errors.New("库存不足")
var ErrCAS = errors.New("version CAS 冲突；本次 Tx 未成功")
var ErrJoin = errors.New("owner 未等到最终 Finished")

// txCleanupFailure 给Rollback失败单独的动态类型。error树即使同时含CAS/1213，
// caller也必须停止重试；“找到预期错误”不能证明同树里的清理失败不存在。
type txCleanupFailure struct{ cause error }

func (e *txCleanupFailure) Error() string { return "Tx清理失败: " + e.cause.Error() }
func (e *txCleanupFailure) Unwrap() error { return e.cause }

// retryable 由CAS/死锁caller分类，kind为本次允许的未提交原因。UNKNOWN优先，
// Rollback失败也优先；只重试已确定回滚且没有清理缺口的完整事务。
func retryable(err error, kind uint16) bool {
	var cleanup *txCleanupFailure
	if errors.Is(err, ErrUnknown) || errors.As(err, &cleanup) {
		return false
	}
	if kind == 0 {
		// kind0是本课CAS分类入口，非MySQL服务器错误码；CAS影响0行无SQL error。
		return errors.Is(err, ErrCAS)
	}
	return code(err) == kind
}

// transact 是本课正常业务的 Tx owner；fn 同步借用 Tx，不可 Commit/留下资源。
// Begin 成功就安排 Rollback，fn 完整成功才显式 Commit；panic 也会执行 Rollback
// 再向上展开。M21 已完整教 panic/未知提交，此处不重复制造那套协议故障。
func transact(ctx context.Context, conn *sql.Conn, fn func(*sql.Tx) error) (err error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if v := recover(); v != nil {
			if e := rollback(tx); e != nil {
				panic(errors.Join(panicError(v), &txCleanupFailure{e}))
			}
			panic(v)
		}
		if cleanup := rollback(tx); cleanup != nil {
			err = errors.Join(err, &txCleanupFailure{cleanup})
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrUnknown, err)
	}
	return nil
}

// code 用 driver 的动态错误类型取真实 MySQL number；errors.As 穿过包装/Join。
// 错误文案会随语言/版本改变，1213与1205的策略应按错误码和业务阶段区分。
func code(err error) uint16 {
	var m *mysql.MySQLError
	if errors.As(err, &m) {
		return m.Number
	}
	return 0
}

// cleanupContext 每次建立独立8秒预算。取消通知不能缩短 owner 的实际 join。
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 8*time.Second)
}
