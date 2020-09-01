package sqlutil

import (
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"go.uber.org/atomic"
)

// ExclusiveWriter implements sqlutil.Writer.
// ExclusiveWriter allows queuing database writes so that you don't
// contend on database locks in, e.g. SQLite. Only one task will run
// at a time on a given ExclusiveWriter.
type ExclusiveWriter struct {
	running  atomic.Bool
	todo     chan transactionWriterTask
	writerID int
}

func NewExclusiveWriter() Writer {
	return &ExclusiveWriter{
		todo: make(chan transactionWriterTask),
	}
}

// transactionWriterTask represents a specific task.
type transactionWriterTask struct {
	db   *sql.DB
	txn  *sql.Tx
	f    func(txn *sql.Tx) error
	wait chan error
}

func (w *ExclusiveWriter) Safe() string {
	a := goid()
	b := w.writerID
	if a == b {
		return ""
	}
	return fmt.Sprintf("%v != %v", a, b)
}

// Do queues a task to be run by a TransactionWriter. The function
// provided will be ran within a transaction as supplied by the
// txn parameter if one is supplied, and if not, will take out a
// new transaction from the database supplied in the database
// parameter. Either way, this will block until the task is done.
func (w *ExclusiveWriter) Do(db *sql.DB, txn *sql.Tx, f func(txn *sql.Tx) error) error {
	if w.todo == nil {
		return errors.New("not initialised")
	}
	if !w.running.Load() {
		go w.run()
	}
	task := transactionWriterTask{
		db:   db,
		txn:  txn,
		f:    f,
		wait: make(chan error, 1),
	}
	w.todo <- task
	return <-task.wait
}

// run processes the tasks for a given transaction writer. Only one
// of these goroutines will run at a time. A transaction will be
// opened using the database object from the task and then this will
// be passed as a parameter to the task function.
func (w *ExclusiveWriter) run() {
	if !w.running.CAS(false, true) {
		return
	}
	w.writerID = goid()
	defer w.running.Store(false)
	for task := range w.todo {
		if task.db != nil && task.txn != nil {
			task.wait <- task.f(task.txn)
		} else if task.db != nil && task.txn == nil {
			task.wait <- WithTransaction(task.db, func(txn *sql.Tx) error {
				return task.f(txn)
			})
		} else {
			task.wait <- task.f(nil)
		}
		close(task.wait)
	}
}

func goid() int {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	idField := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	id, err := strconv.Atoi(idField)
	if err != nil {
		panic(fmt.Sprintf("cannot get goroutine id: %v", err))
	}
	return id
}