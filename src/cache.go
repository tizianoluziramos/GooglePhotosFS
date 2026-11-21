package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

type Cache struct{ db *sql.DB }

type cursor struct {
	state, page, base               string
	complete                        bool
	last, revision, catalogRevision int64
}

var errCacheChanged = errors.New("the index changed during synchronization")

func openCache(filename, account string) (*Cache, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	path, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Cache{db}
	if err = c.init(account); err != nil {
		db.Close()
		return nil, err
	}
	return c, nil
}

func schemaParts() (columns, projection, updates, placeholders string) {
	var types, exprs, sets, binds []string
	for i, value := range (Media{}).fields() {
		name := mediaColumns[i]
		typ, fallback := "INTEGER", "0"
		switch value.(type) {
		case string:
			typ, fallback = "TEXT", "''"
		case float64:
			typ = "REAL"
		}
		types = append(types, name+" "+typ)
		exprs = append(exprs, "COALESCE("+name+","+fallback+")")
		if i != 0 {
			sets = append(sets, name+"=excluded."+name)
		}
		binds = append(binds, "?")
	}
	types[0] += " PRIMARY KEY"
	return strings.Join(types, ","), strings.Join(exprs, ","), strings.Join(sets, ","), strings.Join(binds, ",")
}

var mediaSchema, mediaProjection, mediaUpdates, mediaBinds = schemaParts()
var mediaUpsert = "INSERT INTO remote_media(" + strings.Join(mediaColumns, ",") + ") VALUES(" + mediaBinds + ") ON CONFLICT(media_key) DO UPDATE SET " + mediaUpdates

func (c *Cache) init(account string) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("CREATE TABLE IF NOT EXISTS remote_media(" + mediaSchema + ");" +
		"CREATE TABLE IF NOT EXISTS state(id INTEGER PRIMARY KEY CHECK(id=1), state_token TEXT, page_token TEXT, init_complete INTEGER, last_sync_time INTEGER DEFAULT 0, page_base TEXT DEFAULT '', revision INTEGER DEFAULT 0);" +
		"CREATE INDEX IF NOT EXISTS idx_file_name ON remote_media(file_name);" +
		"CREATE INDEX IF NOT EXISTS gphotosfs_dedup ON remote_media(dedup_key)"); err != nil {
		return err
	}
	for _, migration := range []struct{ table, column, typ string }{
		{"remote_media", "sha1_hash", "TEXT"}, {"state", "last_sync_time", "INTEGER DEFAULT 0"},
		{"state", "page_base", "TEXT DEFAULT ''"}, {"state", "revision", "INTEGER DEFAULT 0"},
		{"state", "catalog_revision", "INTEGER DEFAULT 0"}, {"state", "account", "TEXT DEFAULT ''"},
	} {
		rows, err := tx.Query("SELECT name FROM pragma_table_info(?) WHERE name=?", migration.table, migration.column)
		if err != nil {
			return err
		}
		found := rows.Next()
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		if !found {
			if _, err = tx.Exec("ALTER TABLE " + migration.table + " ADD COLUMN " + migration.column + " " + migration.typ); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec("INSERT OR IGNORE INTO state(id,state_token,page_token,init_complete) VALUES(1,'','',0)"); err != nil {
		return err
	}
	var stored string
	if err = tx.QueryRow("SELECT account FROM state WHERE id=1").Scan(&stored); err != nil {
		return err
	}
	if stored != "" && stored != account {
		return errors.New("this index belongs to another Google Photos account")
	}
	if stored == "" {
		if _, err = tx.Exec("UPDATE state SET account=? WHERE id=1", account); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Cache) cursor(ctx context.Context) (s cursor, err error) {
	err = c.db.QueryRowContext(ctx, "SELECT COALESCE(state_token,''), COALESCE(page_token,''), page_base, COALESCE(init_complete,0), COALESCE(last_sync_time,0), revision, catalog_revision FROM state WHERE id=1").
		Scan(&s.state, &s.page, &s.base, &s.complete, &s.last, &s.revision, &s.catalogRevision)
	return
}

func (c *Cache) apply(ctx context.Context, old, cursor cursor, update LibraryPage) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "UPDATE state SET state_token=?,page_token=?,page_base=?,init_complete=?,last_sync_time=?,revision=revision+1,catalog_revision=catalog_revision+? WHERE id=1 AND revision=?",
		cursor.state, cursor.page, cursor.base, cursor.complete, cursor.last, len(update.Items)+len(update.Deleted) > 0, old.revision)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errCacheChanged
	}
	if len(update.Items) != 0 {
		stmt, err := tx.PrepareContext(ctx, mediaUpsert)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, item := range update.Items {
			if _, err = stmt.ExecContext(ctx, item.fields()...); err != nil {
				return err
			}
		}
	}
	if len(update.Deleted) != 0 {
		stmt, err := tx.PrepareContext(ctx, "DELETE FROM remote_media WHERE media_key=?")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, key := range update.Deleted {
			if _, err = stmt.ExecContext(ctx, key); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (c *Cache) list(ctx context.Context) ([]Media, int64, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRowContext(ctx, "SELECT catalog_revision FROM state WHERE id=1").Scan(&revision); err != nil {
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+mediaProjection+" FROM remote_media WHERE COALESCE(trash_timestamp,0)=0 ORDER BY utc_timestamp DESC,media_key")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var items []Media
	for rows.Next() {
		var item Media
		if err = rows.Scan(item.destinations()...); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, revision, rows.Err()
}

func (c *Cache) media(ctx context.Context, key, dedup string) (m Media, err error) {
	err = c.db.QueryRowContext(ctx, "SELECT "+mediaProjection+
		" FROM remote_media WHERE (media_key=? OR (dedup_key=? AND dedup_key<>'')) AND COALESCE(trash_timestamp,0)=0 ORDER BY is_canonical DESC, media_key=? DESC LIMIT 1",
		key, dedup, key).Scan(m.destinations()...)
	return
}

func (c *Cache) saveMedia(ctx context.Context, item Media, trash bool) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if trash {
		_, err = tx.ExecContext(ctx, "UPDATE remote_media SET trash_timestamp=? WHERE media_key=? OR (dedup_key=? AND dedup_key<>'')", item.Trashed, item.Key, item.Dedup)
	} else {
		_, err = tx.ExecContext(ctx, mediaUpsert, item.fields()...)
		if err == nil && item.Dedup != "" {
			_, err = tx.ExecContext(ctx, "UPDATE remote_media SET caption=? WHERE dedup_key=? AND media_key<>?", item.Caption, item.Dedup, item.Key)
		}
	}
	if err != nil {
		return fmt.Errorf("save index: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE state SET revision=revision+1,catalog_revision=catalog_revision+1,last_sync_time=0 WHERE id=1"); err != nil {
		return err
	}
	return tx.Commit()
}
