package cache

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMattermostMigrationCreatesVersionedSchemaWithoutChangingLegacyRows(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "cache.db")
	db, err := New(dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO workspaces (id, name) VALUES ('legacy', 'Legacy Slack')`); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = New(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.conn.QueryRow(`SELECT version FROM cache_schema_versions WHERE component = 'mattermost'`).Scan(&version); err != nil {
		t.Fatalf("Mattermost schema version: %v", err)
	}
	if version != 1 {
		t.Fatalf("Mattermost schema version = %d, want 1", version)
	}

	for _, table := range []string{
		"mattermost_servers",
		"mattermost_teams",
		"mattermost_users",
		"mattermost_channels",
		"mattermost_channel_memberships",
		"mattermost_channel_users",
		"mattermost_posts",
	} {
		var found int
		if err := db.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil {
			t.Fatalf("find table %s: %v", table, err)
		}
		if found != 1 {
			t.Errorf("table %s count = %d, want 1", table, found)
		}
	}

	var legacyName string
	if err := db.conn.QueryRow(`SELECT name FROM workspaces WHERE id = 'legacy'`).Scan(&legacyName); err != nil {
		t.Fatalf("legacy row after Mattermost migration: %v", err)
	}
	if legacyName != "Legacy Slack" {
		t.Fatalf("legacy workspace name = %q", legacyName)
	}
}

func TestMattermostMigrationCreatesRequiredIndexes(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	indexes := []string{
		"idx_mattermost_teams_server",
		"idx_mattermost_users_server",
		"idx_mattermost_channels_server",
		"idx_mattermost_channels_team",
		"idx_mattermost_posts_channel_chronology",
		"idx_mattermost_posts_thread",
		"idx_mattermost_memberships_user",
		"idx_mattermost_memberships_channel",
		"idx_mattermost_channel_users_user",
		"idx_mattermost_channel_users_channel",
	}
	for _, index := range indexes {
		var found int
		if err := db.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&found); err != nil {
			t.Fatalf("find index %s: %v", index, err)
		}
		if found != 1 {
			t.Errorf("index %s count = %d, want 1", index, found)
		}
	}

	wantColumns := map[string][]string{
		"idx_mattermost_posts_channel_chronology": {"server_id", "channel_id", "created_at", "id"},
		"idx_mattermost_posts_thread":             {"server_id", "channel_id", "root_id", "created_at", "id"},
	}
	for index, want := range wantColumns {
		rows, err := db.conn.Query(`SELECT name FROM pragma_index_info(?) ORDER BY seqno`, index)
		if err != nil {
			t.Fatalf("pragma_index_info(%s): %v", index, err)
		}
		var got []string
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				rows.Close()
				t.Fatalf("scan index %s: %v", index, err)
			}
			got = append(got, column)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("index %s columns = %v, want %v", index, got, want)
		}
	}
}

func TestMattermostMigrationEnforcesChannelAndForeignKeyConstraints(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	if _, err := db.conn.Exec(`INSERT INTO mattermost_servers (id, name, url, current_user_id, last_synced_at) VALUES ('s1', 'One', 'https://one.example', 'u1', 0)`); err != nil {
		t.Fatalf("insert server: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO mattermost_teams (server_id, id, name, display_name) VALUES ('s1', 't1', 'team', 'Team')`); err != nil {
		t.Fatalf("insert team: %v", err)
	}

	invalidStatements := []string{
		`INSERT INTO mattermost_channels (server_id, id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at) VALUES ('s1', 'bad-kind', 't1', '', '', 'other', 0, 0, 0)`,
		`INSERT INTO mattermost_channels (server_id, id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at) VALUES ('s1', 'public-no-team', NULL, '', '', 'public', 0, 0, 0)`,
		`INSERT INTO mattermost_channels (server_id, id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at) VALUES ('missing', 'bad-server', NULL, '', '', 'direct', 0, 0, 0)`,
		`INSERT INTO mattermost_channels (server_id, id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at) VALUES ('s1', 'negative', NULL, '', '', 'direct', -1, 0, 0)`,
	}
	for _, statement := range invalidStatements {
		if _, err := db.conn.Exec(statement); err == nil {
			t.Errorf("constraint accepted invalid statement: %s", statement)
		}
	}

	if _, err := db.conn.Exec(`INSERT INTO mattermost_channels (server_id, id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at) VALUES ('s1', 'direct-with-team', 't1', '', '', 'direct', 0, 0, 0)`); err != nil {
		t.Fatalf("direct channel with supplied team should be accepted: %v", err)
	}
}

func TestMattermostMigrationRollsBackDDLAndVersionOnFailure(t *testing.T) {
	conn, err := sql.Open("sqlite", appendPragmas(":memory:"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	db := &DB{conn: conn}

	err = db.migrateMattermost([]mattermostMigration{{
		version: 1,
		statements: []string{
			`CREATE TABLE rollback_probe (id TEXT PRIMARY KEY)`,
			`THIS IS NOT SQL`,
		},
	}})
	if err == nil {
		t.Fatal("migrateMattermost returned nil error")
	}

	var found int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rollback_probe'`).Scan(&found); err != nil {
		t.Fatalf("find rollback probe: %v", err)
	}
	if found != 0 {
		t.Fatalf("rollback_probe exists after failed migration")
	}
	var version int
	err = conn.QueryRow(`SELECT version FROM cache_schema_versions WHERE component = 'mattermost'`).Scan(&version)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("version query error = %v, want sql.ErrNoRows", err)
	}
}

func TestMattermostMigrationRejectsMalformedSequences(t *testing.T) {
	tests := []struct {
		name       string
		migrations []mattermostMigration
		want       string
	}{
		{"empty", nil, "must end at version 1"},
		{"starts above one", []mattermostMigration{{version: 2}}, "start at version 1"},
		{"gap", []mattermostMigration{{version: 1}, {version: 3}}, "contiguous"},
		{"duplicate", []mattermostMigration{{version: 1}, {version: 1}}, "contiguous"},
		{"beyond current", []mattermostMigration{{version: 1}, {version: 2}}, "newer than supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := New(":memory:")
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer db.Close()
			err = db.migrateMattermost(test.migrations)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestMattermostMigrationConcurrentNewDatabaseOpens(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "cache.db")
	start := make(chan struct{})
	errs := make(chan error, 2)
	dbs := make(chan *DB, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			db, err := New(dsn)
			if err == nil {
				dbs <- db
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(dbs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent New: %v", err)
		}
	}
	for db := range dbs {
		_ = db.Close()
	}
	if t.Failed() {
		return
	}

	db, err := New(dsn)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	var count, version int
	if err := db.conn.QueryRow(`SELECT COUNT(*), MAX(version) FROM cache_schema_versions WHERE component = 'mattermost'`).Scan(&count, &version); err != nil {
		t.Fatalf("version row: %v", err)
	}
	if count != 1 || version != mattermostSchemaVersion {
		t.Fatalf("version row = count %d version %d", count, version)
	}
}
