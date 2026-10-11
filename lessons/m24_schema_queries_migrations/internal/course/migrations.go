package course

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var errOldWriters = errors.New("仍有旧版写者，禁止 contract")

// migrations 由 Run 最后调用，拥有独占版本表/evolve/audit，展示一次兼容演进。
// 每个DDL只改变本run表；版本号在结构后置条件通过后写入，不冒充DDL+ledger同一事务。
func (l *lesson) migrations(ctx context.Context) (err error) {
	if err = l.create(ctx, "versions", `(version INT NOT NULL PRIMARY KEY,description VARCHAR(100) NOT NULL) ENGINE=InnoDB`); err != nil {
		return err
	}
	if err = l.create(ctx, "evolve", `(id BIGINT NOT NULL PRIMARY KEY,amount DECIMAL(12,2) NOT NULL,note VARCHAR(100) NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`); err != nil {
		return err
	}
	if _, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("versions")+" VALUES (1,'v1 amount/note')"); err != nil {
		return err
	}
	if _, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("evolve")+" VALUES (1,'0.10',NULL),(2,'1.20','放门口')"); err != nil {
		return err
	}
	if err = l.expand(ctx); err != nil {
		return err
	}
	if err = l.expand(ctx); err != nil {
		return err
	} // 相同迁移重复执行只核对事实，不重复ADD。
	// 有界制造“DDL成功、ledger尚未写”的恢复状态。这里只删自己的版本2记录，
	// 不声称真的杀进程；重新进入expand必须读到现存列并补账，不能再次ADD撞重复列。
	if _, err = l.db.ExecContext(ctx, "DELETE FROM "+l.table("versions")+" WHERE version=2"); err != nil {
		return err
	}
	if err = l.expand(ctx); err != nil {
		return err
	}
	fmt.Fprintln(l.out, "[migration recovery] existing-expanded-column=true missing-ledger-reconciled=true no-second-ADD=true")
	if err = l.backfill(ctx); err != nil {
		return err
	}
	// v1应用仍只写note；expand允许note_v2为空，否则滚动发布中的旧程序立即失败。
	if _, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("evolve")+" (id,amount,note) VALUES (3,'2.30','旧版刚写')"); err != nil {
		return err
	}
	// 新版先双写，让仍在服务中的旧读者看见同一个业务值；不是仅填新列就删除旧列。
	if _, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("evolve")+" (id,amount,note,note_v2) VALUES (4,'3.40','新版双写','新版双写')"); err != nil {
		return err
	}
	var oldRead, newRead string
	if err = l.db.QueryRowContext(ctx, "SELECT note FROM "+l.table("evolve")+" WHERE id=3").Scan(&oldRead); err != nil {
		return err
	}
	if err = l.db.QueryRowContext(ctx, "SELECT COALESCE(note_v2,note,'') FROM "+l.table("evolve")+" WHERE id=3").Scan(&newRead); err != nil {
		return err
	}
	if err = require(oldRead == "旧版刚写" && newRead == oldRead, "expand时旧写/旧读/新fallback读必须兼容"); err != nil {
		return err
	}
	_, tightenErr := l.db.ExecContext(ctx, "ALTER TABLE "+l.table("evolve")+" MODIFY note_v2 VARCHAR(100) NOT NULL")
	if err = expectCode(tightenErr, 1138); err != nil {
		return err
	}
	nulls, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("evolve")+" WHERE note_v2 IS NULL")
	if err != nil {
		return err
	}
	nullable, err := l.columnNullable(ctx, "evolve", "note_v2")
	if err != nil {
		return err
	}
	versions, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("versions"))
	if err != nil {
		return err
	}
	if err = require(nulls == 1 && nullable == "YES" && versions == 2, "失败收紧后必须准确保留数据/nullable/版本2"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[migration failed accurately] NOT_NULL code=1138 null_rows=%d nullable=%s ledger_rows=%d old-read=%q new-read=%q\n", nulls, nullable, versions, oldRead, newRead)
	if err = l.contract(ctx, false); !errors.Is(err, errOldWriters) {
		return fmt.Errorf("应挡住旧写者尚在场的contract: %w", err)
	}
	fmt.Fprintln(l.out, "[contract gate] old-writers-active=true SQL-not-issued=true")
	if err = l.backfill(ctx); err != nil {
		return err
	}
	// 本实验已停止旧写函数，完成第二次回填；生产此条件来自发布/消费者证据，不来自布尔猜测。
	if err = l.contract(ctx, true); err != nil {
		return err
	}
	if err = l.expand(ctx); err != nil {
		return err
	}
	if err = l.contract(ctx, true); err != nil {
		return err
	}
	var after string
	if err = l.db.QueryRowContext(ctx, "SELECT note_v2 FROM "+l.table("evolve")+" WHERE id=3").Scan(&after); err != nil {
		return err
	}
	var gone string
	oldErr := l.db.QueryRowContext(ctx, "SELECT note FROM "+l.table("evolve")+" WHERE id=3").Scan(&gone)
	if err = expectCode(oldErr, 1054); err != nil {
		return err
	}
	n, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("evolve"))
	if err != nil {
		return err
	}
	versions, err = l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("versions"))
	if err != nil {
		return err
	}
	if err = require(n == 4 && versions == 3 && after == "旧版刚写", "contract后数据4行/版本3/新版值必须保持"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[contract complete] data_rows=%d ledger_rows=%d new-read=%q old-SQL=1054 replay=no-DDL\n", n, versions, after)
	if err = l.implicitCommit(ctx); err != nil {
		return err
	}
	return l.metadataLock(ctx)
}

// ledger 查询一个版本是否记录；ErrNoRows是“未记录”，其他错误不能降格成可重试迁移。
func (l *lesson) ledger(ctx context.Context, version int) (bool, error) {
	var v int
	err := l.db.QueryRowContext(ctx, "SELECT version FROM "+l.table("versions")+" WHERE version=?", version).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// columnNullable 读information_schema结构事实。不存在时返回ErrNoRows，不能当作NOT NULL。
func (l *lesson) columnNullable(ctx context.Context, suffix, column string) (string, error) {
	var state string
	err := l.db.QueryRowContext(ctx, "SELECT IS_NULLABLE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? AND COLUMN_NAME=?", l.database, l.names[suffix], column).Scan(&state)
	return state, err
}

// expand 是版本2：新增可空列，兼容旧程序；已存在但未记账时按结构核验补记。
// 它只证明单owner可重复执行，不声称多部署者迁移锁/校验和/完整生产恢复框架。
func (l *lesson) expand(ctx context.Context) error {
	applied, err := l.ledger(ctx, 2)
	if err != nil {
		return err
	}
	_, shapeErr := l.columnNullable(ctx, "evolve", "note_v2")
	if applied {
		if shapeErr != nil {
			return fmt.Errorf("版本2已记账但列不存在: %w", shapeErr)
		}
		fmt.Fprintln(l.out, "[migration v2] already-recorded; shape-verified=true")
		return nil
	}
	if errors.Is(shapeErr, sql.ErrNoRows) {
		query := "ALTER TABLE " + l.table("evolve") + " ADD note_v2 VARCHAR(100) NULL"
		fmt.Fprintln(l.out, "[SQL expand]", query)
		if _, err = l.db.ExecContext(ctx, query); err != nil {
			return err
		}
	} else if shapeErr != nil {
		return shapeErr
	}
	state, err := l.columnNullable(ctx, "evolve", "note_v2")
	if err != nil {
		return err
	}
	if err = require(state == "YES", "版本2新增列应为可空，兼容旧写者"); err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("versions")+" VALUES (2,'expand nullable note_v2')")
	return err
}

// backfill 由迁移owner在每轮兼容写之后调用，NULL→旧值/空串，已填值不覆盖。
// 本课4行一次UPDATE有界；真实大表应按主键分批和预算推进，不把此SQL推广成无界全表作业。
func (l *lesson) backfill(ctx context.Context) error {
	result, err := l.db.ExecContext(ctx, "UPDATE "+l.table("evolve")+" SET note_v2=COALESCE(note,'') WHERE note_v2 IS NULL")
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	n, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("evolve")+" WHERE note_v2 IS NULL")
	if err != nil {
		return err
	}
	if err = require(n == 0, "回填结束必须重新SELECT证明没有NULL"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[backfill] RowsAffected=%d verified-null-rows=%d\n", changed, n)
	return nil
}

// contract 是版本3：调用者提供已停止旧读/写者的发布条件，先核验数据再收紧/删列。
// 两个DDL之间可能中断：重复执行按真实列结构跳过已完成一步，最后才写ledger。
func (l *lesson) contract(ctx context.Context, oldVersionsRetired bool) error {
	if !oldVersionsRetired {
		return errOldWriters
	}
	applied, err := l.ledger(ctx, 3)
	if err != nil {
		return err
	}
	state, err := l.columnNullable(ctx, "evolve", "note_v2")
	if err != nil {
		return err
	}
	_, oldErr := l.columnNullable(ctx, "evolve", "note")
	if applied {
		return require(state == "NO" && errors.Is(oldErr, sql.ErrNoRows), "版本3账本与真实NOT_NULL/旧列删除必须一致")
	}
	n, err := l.scalar(ctx, "SELECT COUNT(*) FROM "+l.table("evolve")+" WHERE note_v2 IS NULL")
	if err != nil {
		return err
	}
	if err = require(n == 0, "必须先回填后收紧"); err != nil {
		return err
	}
	if state != "NO" {
		query := "ALTER TABLE " + l.table("evolve") + " MODIFY note_v2 VARCHAR(100) NOT NULL"
		fmt.Fprintln(l.out, "[SQL tighten]", query)
		if _, err = l.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if oldErr == nil {
		query := "ALTER TABLE " + l.table("evolve") + " DROP COLUMN note"
		fmt.Fprintln(l.out, "[SQL contract]", query)
		if _, err = l.db.ExecContext(ctx, query); err != nil {
			return err
		}
	} else if !errors.Is(oldErr, sql.ErrNoRows) {
		return oldErr
	}
	state, err = l.columnNullable(ctx, "evolve", "note_v2")
	if err != nil {
		return err
	}
	_, oldErr = l.columnNullable(ctx, "evolve", "note")
	if err = require(state == "NO" && errors.Is(oldErr, sql.ErrNoRows), "DDL完成后重新核对结构再记版本"); err != nil {
		return err
	}
	_, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("versions")+" VALUES (3,'contract NOT NULL/drop old note')")
	return err
}

// implicitCommit 使用audit的同一Tx连接实测成功/失败DDL之前的DML。
// sql.Tx只是Go对象，MySQL的ALTER可能已隐式提交；随后Rollback返回nil不表示UPDATE撤销。
func (l *lesson) implicitCommit(ctx context.Context) (err error) {
	if err = l.create(ctx, "audit", `(id INT PRIMARY KEY,value INT NOT NULL) ENGINE=InnoDB`); err != nil {
		return err
	}
	if _, err = l.db.ExecContext(ctx, "INSERT INTO "+l.table("audit")+" VALUES (1,10)"); err != nil {
		return err
	}
	for _, phase := range []struct {
		value int
		ddl   string
		code  uint16
	}{{20, "ADD stamp INT NULL", 0}, {30, "ADD stamp INT NULL", 1060}} {
		tx, beginErr := l.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		// 单独函数限定defer作用域；每轮Tx必须结束，不把循环内资源积到课程结束。
		phaseErr := func() (err error) {
			defer func() { err = errors.Join(err, rollback(tx)) }()
			if _, err = tx.ExecContext(ctx, "UPDATE "+l.table("audit")+" SET value=? WHERE id=1", phase.value); err != nil {
				return err
			}
			_, ddlErr := tx.ExecContext(ctx, "ALTER TABLE "+l.table("audit")+" "+phase.ddl)
			if phase.code == 0 && ddlErr != nil {
				return ddlErr
			}
			if phase.code != 0 {
				if err = expectCode(ddlErr, phase.code); err != nil {
					return err
				}
			}
			return tx.Rollback() // 实际驱动返回被记录；DDL已提交的副作用不会因这句消失。
		}()
		if phaseErr != nil {
			return phaseErr
		}
		value, readErr := l.scalar(ctx, "SELECT value FROM "+l.table("audit")+" WHERE id=1")
		if readErr != nil {
			return readErr
		}
		_, shapeErr := l.columnNullable(ctx, "audit", "stamp")
		if err = require(value == phase.value && shapeErr == nil, "DDL/失败DDL前UPDATE已提交，Rollback不能撤销"); err != nil {
			return err
		}
		fmt.Fprintf(l.out, "[implicit commit] UPDATE=%d DDL-code=%d Rollback=nil persisted-value=%d stamp-exists=true\n", phase.value, phase.code, value)
	}
	return nil
}

// metadataLock 持有一个读事务的metadata lock，另一独占Conn尝试ALTER并在2秒会话预算内失败。
// 观察者按连接ID读PROCESSLIST，只有实际STATE命中才说阻塞；ticker是重复观察，不猜调度。
func (l *lesson) metadataLock(ctx context.Context) (err error) {
	holder, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rollback(holder)) }()
	var value int
	if err = holder.QueryRowContext(ctx, "SELECT value FROM "+l.table("audit")+" WHERE id=1").Scan(&value); err != nil {
		return err
	}
	idReady := make(chan int64, 1)
	t := startTask(ctx, func(work context.Context) (err error) {
		conn, err := l.db.Conn(work)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, conn.Close()) }()
		if _, err = conn.ExecContext(work, "SET SESSION lock_wait_timeout=2"); err != nil {
			return err
		}
		var id int64
		if err = conn.QueryRowContext(work, "SELECT CONNECTION_ID()").Scan(&id); err != nil {
			return err
		}
		idReady <- id // 缓冲1，owner即使提前失败worker也不会卡在结果通知。
		_, err = conn.ExecContext(work, "ALTER TABLE "+l.table("audit")+" ADD lock_probe INT NULL")
		return err
	}, nil)
	defer func() {
		// 先释放holder再cancel/join，提前失败也不能让owner一边等worker一边握着阻塞它的锁。
		err = errors.Join(err, rollback(holder), t.stopAndJoin())
	}()
	var id int64
	select {
	case id = <-idReady:
	case <-t.finished:
		return errors.Join(errors.New("DDL尚未交付connection-id就退出"), t.join(ctx))
	case <-ctx.Done():
		return ctx.Err()
	}
	observe, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	observed := false
	for !observed {
		var state string
		readErr := l.db.QueryRowContext(observe, "SELECT COALESCE(STATE,'') FROM information_schema.PROCESSLIST WHERE ID=?", id).Scan(&state)
		if readErr == nil && strings.Contains(strings.ToLower(state), "metadata lock") {
			observed = true
			fmt.Fprintf(l.out, "[DDL actual wait] connection=%d state=%q holder-value=%d pool-inuse=%d\n", id, state, value, l.db.Stats().InUse)
			break
		}
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return readErr
		}
		select {
		case <-ticker.C:
		case <-observe.Done():
			return fmt.Errorf("未观察到真实metadata lock等待: %w", observe.Err())
		case <-t.finished:
			return errors.Join(errors.New("DDL提前结束，尚未观察到metadata lock"), t.join(ctx))
		}
	}
	workerErr := t.join(ctx)
	if err = expectCode(workerErr, 1205); err != nil {
		return err
	}
	if err = holder.Rollback(); err != nil {
		return err
	}
	_, shapeErr := l.columnNullable(ctx, "audit", "lock_probe")
	if err = require(errors.Is(shapeErr, sql.ErrNoRows), "metadata lock超时DDL不能留下lock_probe列"); err != nil {
		return err
	}
	current, err := l.scalar(ctx, "SELECT value FROM "+l.table("audit")+" WHERE id=1")
	if err != nil {
		return err
	}
	if err = require(current == 30 && l.db.Stats().InUse == 0, "DDL worker已Finished/锁持有者已回滚，数据30与池释放必须读回"); err != nil {
		return err
	}
	fmt.Fprintf(l.out, "[DDL lock finished] code=1205 lock_probe-exists=false value=%d inuse=0 observed-wait=true\n", current)
	return nil
}
