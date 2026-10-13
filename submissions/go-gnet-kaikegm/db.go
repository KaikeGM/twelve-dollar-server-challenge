package main

/*
#cgo CFLAGS: -O3 -DNDEBUG -fomit-frame-pointer -finline-functions -DSQLITE_THREADSAFE=2 -DSQLITE_DEFAULT_MEMSTATUS=0 -DSQLITE_OMIT_LOAD_EXTENSION -DSQLITE_OMIT_DEPRECATED -DSQLITE_DEFAULT_WAL_SYNCHRONOUS=1 -DSQLITE_DQS=0 -DSQLITE_ENABLE_MATH_FUNCTIONS=0 -DSQLITE_TEMP_STORE=3 -DSQLITE_USE_ALLOCA=1 -DSQLITE_DIRECT_OVERFLOW_READ=1 -DSQLITE_OMIT_PROGRESS_CALLBACK=1 -DSQLITE_OMIT_SHARED_CACHE=1 -DSQLITE_OMIT_DECLTYPE=1 -DSQLITE_OMIT_EXPLAIN=1 -DSQLITE_OMIT_AUTHORIZATION=1 -DSQLITE_MAX_MMAP_SIZE=2147483648 -DHAVE_USLEEP=1
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include <string.h>
#include "sqlite/sqlite3.h"

// q_text: copies column 0 of the first row into buf. Single CGO call.
static int q_text(sqlite3_stmt *s, sqlite3_int64 arg, char *buf, int cap, int *n) {
	if (arg > 0) sqlite3_bind_int64(s, 1, arg);
	int rc = sqlite3_step(s);
	if (rc == SQLITE_ROW) {
		const unsigned char *t = sqlite3_column_text(s, 0);
		int len = sqlite3_column_bytes(s, 0);
		if (len <= cap) memcpy(buf, t, len);
		*n = len;
	}
	sqlite3_reset(s);
	return rc;
}

// q_insert: INSERT ... RETURNING id, created_at. Steps to SQLITE_DONE so the row is committed.
static int q_insert(sqlite3_stmt *s, sqlite3_int64 uid, const char *body, int blen,
                    sqlite3_int64 *id, char *created, int cap, int *n) {
	sqlite3_bind_int64(s, 1, uid);
	sqlite3_bind_text(s, 2, body, blen, SQLITE_TRANSIENT);
	int rc = sqlite3_step(s);
	if (rc == SQLITE_ROW) {
		*id = sqlite3_column_int64(s, 0);
		const unsigned char *t = sqlite3_column_text(s, 1);
		int len = sqlite3_column_bytes(s, 1);
		if (len > cap) len = cap;
		memcpy(created, t, len);
		*n = len;
		rc = sqlite3_step(s);
	}
	sqlite3_reset(s);
	return rc;
}

// q_like: binds (uid, pid, pid), checks changes; if 0, steps s_exists in C.
static int q_like(sqlite3_stmt *s_like, sqlite3_stmt *s_exists, sqlite3_int64 uid, sqlite3_int64 pid, int *inserted, int *exists) {
	sqlite3_bind_int64(s_like, 1, uid);
	sqlite3_bind_int64(s_like, 2, pid);
	sqlite3_bind_int64(s_like, 3, pid);
	int rc = sqlite3_step(s_like);
	int ch = sqlite3_changes(sqlite3_db_handle(s_like));
	sqlite3_reset(s_like);
	if (rc != SQLITE_DONE) return rc;
	if (ch == 1) {
		*inserted = 1;
		*exists = 1;
		return SQLITE_DONE;
	}
	*inserted = 0;
	sqlite3_bind_int64(s_exists, 1, pid);
	int rc2 = sqlite3_step(s_exists);
	*exists = (rc2 == SQLITE_ROW) ? 1 : 0;
	sqlite3_reset(s_exists);
	return (rc2 == SQLITE_ROW || rc2 == SQLITE_DONE) ? SQLITE_DONE : rc2;
}

static int prep(sqlite3 *db, const char *sql, sqlite3_stmt **s) {
	return sqlite3_prepare_v3(db, sql, -1, SQLITE_PREPARE_PERSISTENT, s, 0);
}

static void bg_checkpoint(sqlite3 *db) {
	sqlite3_wal_checkpoint_v2(db, NULL, SQLITE_CHECKPOINT_PASSIVE, NULL, NULL);
}
*/
import "C"

import (
	"errors"
	"log"
	"slices"
	"time"
	"unsafe"
)

const postObj = `json_object('id', p.id, 'body', p.body, 'created_at', p.created_at, 'author', u.username,
 'like_count', (SELECT count(1) FROM likes WHERE post_id = p.id))`

const (
	feedSQL   = `WITH p AS (SELECT id, user_id, body, created_at FROM posts ORDER BY created_at DESC, id DESC LIMIT 20) SELECT '{"posts":[' || coalesce(group_concat(` + postObj + `, ','), '') || ']}' FROM p JOIN users u ON u.id = p.user_id`
	postSQL   = `SELECT '{"post":' || ` + postObj + ` || '}' FROM posts p JOIN users u ON u.id = p.user_id WHERE p.id = ?`
	existsSQL = "SELECT 1 FROM posts WHERE id = ?"
	insertSQL = "INSERT INTO posts (user_id, body) VALUES (?, ?) RETURNING id, created_at"
	likeSQL   = "INSERT INTO likes (user_id, post_id) SELECT ?, ? WHERE EXISTS (SELECT 1 FROM posts WHERE id = ?) ON CONFLICT DO NOTHING"
	pragmas   = "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000; PRAGMA mmap_size=1073741824; PRAGMA cache_size=-2048; PRAGMA temp_store=MEMORY; PRAGMA automatic_index=0; PRAGMA threads=0; PRAGMA cell_size_check=0; PRAGMA foreign_keys=OFF; PRAGMA secure_delete=0;"
)

var (
	// Main database handle: accessed exclusively by the single-threaded event loop (0 locks, 0 mutex contention!)
	db                                           *C.sqlite3
	feedS, postS, existsS, insertS, likeS, pingS *C.sqlite3_stmt
)

func dbErr() error { return errors.New(C.GoString(C.sqlite3_errmsg(db))) }

func dbOpen(path string) {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	if C.sqlite3_open_v2(cpath, &db, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_NOMUTEX, nil) != C.SQLITE_OK {
		log.Fatal("open: ", dbErr())
	}
	cp := C.CString(pragmas)
	defer C.free(unsafe.Pointer(cp))
	if C.sqlite3_exec(db, cp, nil, nil, nil) != C.SQLITE_OK {
		log.Fatal("pragmas: ", dbErr())
	}

	for _, p := range []struct {
		s   **C.sqlite3_stmt
		sql string
	}{
		{&feedS, feedSQL},
		{&postS, postSQL},
		{&existsS, existsSQL},
		{&insertS, insertSQL},
		{&likeS, likeSQL},
		{&pingS, "SELECT 1"},
	} {
		cs := C.CString(p.sql)
		rc := C.prep(db, cs, p.s)
		C.free(unsafe.Pointer(cs))
		if rc != C.SQLITE_OK {
			log.Fatal("prepare: ", dbErr())
		}
	}

	// Separate connection for background passive WAL checkpointing:
	// Runs on its own handle so the event loop never locks or waits!
	var chkDB *C.sqlite3
	if C.sqlite3_open_v2(cpath, &chkDB, C.SQLITE_OPEN_READWRITE|C.SQLITE_OPEN_NOMUTEX, nil) == C.SQLITE_OK {
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if chkDB != nil {
					C.bg_checkpoint(chkDB)
				}
			}
		}()
	}
}

// text runs a one-column query straight into out's spare capacity (zero locks, zero mutex).
func text(s *C.sqlite3_stmt, arg int64, out []byte) ([]byte, bool, error) {
	for {
		if cap(out)-len(out) < 8192 {
			out = slices.Grow(out, 16<<10)
		}
		spare := out[len(out):cap(out)]
		var n C.int
		rc := C.q_text(s, C.sqlite3_int64(arg), (*C.char)(unsafe.Pointer(&spare[0])), C.int(len(spare)), &n)
		switch {
		case rc == C.SQLITE_DONE:
			return out, false, nil
		case rc != C.SQLITE_ROW:
			return out, false, dbErr()
		case int(n) > len(spare):
			out = slices.Grow(out, int(n))
			continue
		}
		return out[:len(out)+int(n)], true, nil
	}
}

func dbFeed(out []byte) ([]byte, error) {
	b, _, err := text(feedS, 0, out)
	return b, err
}

func dbPost(out []byte, id int64) ([]byte, bool, error) { return text(postS, id, out) }

func dbPing() error {
	_, _, err := text(pingS, 0, nil)
	return err
}

func dbInsert(uid int64, body string) (int64, string, error) {
	var id C.sqlite3_int64
	var created [64]byte
	var n C.int
	rc := C.q_insert(insertS, C.sqlite3_int64(uid), (*C.char)(unsafe.Pointer(unsafe.StringData(body))), C.int(len(body)),
		&id, (*C.char)(unsafe.Pointer(&created[0])), C.int(len(created)), &n)
	if rc != C.SQLITE_DONE {
		return 0, "", dbErr()
	}
	return int64(id), string(created[:n]), nil
}

func dbLike(uid, id int64) (inserted, exists bool, err error) {
	var ins, ex C.int
	rc := C.q_like(likeS, existsS, C.sqlite3_int64(uid), C.sqlite3_int64(id), &ins, &ex)
	if rc != C.SQLITE_DONE {
		return false, false, dbErr()
	}
	return ins == 1, ex == 1, nil
}
