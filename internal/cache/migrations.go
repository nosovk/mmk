package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const mattermostSchemaVersion = 2

type mattermostMigration struct {
	version    int
	statements []string
}

var mattermostMigrations = []mattermostMigration{
	{
		version: 1,
		statements: []string{
			`CREATE TABLE mattermost_servers (
			id TEXT PRIMARY KEY CHECK (id <> ''),
			name TEXT NOT NULL,
			url TEXT NOT NULL CHECK (url <> ''),
			current_user_id TEXT NOT NULL CHECK (current_user_id <> ''),
			last_synced_at INTEGER NOT NULL CHECK (last_synced_at >= 0)
		)`,
			`CREATE TABLE mattermost_teams (
			server_id TEXT NOT NULL CHECK (server_id <> ''),
			id TEXT NOT NULL CHECK (id <> ''),
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			PRIMARY KEY (server_id, id),
			FOREIGN KEY (server_id) REFERENCES mattermost_servers(id) ON DELETE CASCADE
		)`,
			`CREATE TABLE mattermost_users (
			server_id TEXT NOT NULL CHECK (server_id <> ''),
			id TEXT NOT NULL CHECK (id <> ''),
			username TEXT NOT NULL,
			nickname TEXT NOT NULL,
			first_name TEXT NOT NULL,
			last_name TEXT NOT NULL,
			updated_at INTEGER NOT NULL CHECK (updated_at >= 0),
			PRIMARY KEY (server_id, id),
			FOREIGN KEY (server_id) REFERENCES mattermost_servers(id) ON DELETE CASCADE
		)`,
			`CREATE TABLE mattermost_channels (
			server_id TEXT NOT NULL CHECK (server_id <> ''),
			id TEXT NOT NULL CHECK (id <> ''),
			team_id TEXT,
			name TEXT NOT NULL,
			display_name TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('public', 'private', 'direct', 'group')),
			total_msg_count INTEGER NOT NULL CHECK (total_msg_count >= 0),
			updated_at INTEGER NOT NULL CHECK (updated_at >= 0),
			deleted_at INTEGER NOT NULL CHECK (deleted_at >= 0),
			PRIMARY KEY (server_id, id),
			FOREIGN KEY (server_id) REFERENCES mattermost_servers(id) ON DELETE CASCADE,
			FOREIGN KEY (server_id, team_id) REFERENCES mattermost_teams(server_id, id) ON DELETE CASCADE,
			CHECK (kind NOT IN ('public', 'private') OR team_id IS NOT NULL)
		)`,
			`CREATE TABLE mattermost_channel_memberships (
			server_id TEXT NOT NULL CHECK (server_id <> ''),
			channel_id TEXT NOT NULL CHECK (channel_id <> ''),
			user_id TEXT NOT NULL CHECK (user_id <> ''),
			msg_count INTEGER NOT NULL CHECK (msg_count >= 0),
			mention_count INTEGER NOT NULL CHECK (mention_count >= 0),
			last_viewed_at INTEGER NOT NULL CHECK (last_viewed_at >= 0),
			updated_at INTEGER NOT NULL CHECK (updated_at >= 0),
			PRIMARY KEY (server_id, channel_id, user_id),
			FOREIGN KEY (server_id, channel_id) REFERENCES mattermost_channels(server_id, id) ON DELETE CASCADE
		)`,
			`CREATE TABLE mattermost_channel_users (
			server_id TEXT NOT NULL CHECK (server_id <> ''),
			channel_id TEXT NOT NULL CHECK (channel_id <> ''),
			user_id TEXT NOT NULL CHECK (user_id <> ''),
			PRIMARY KEY (server_id, channel_id, user_id),
			FOREIGN KEY (server_id, channel_id) REFERENCES mattermost_channels(server_id, id) ON DELETE CASCADE
		)`,
			`CREATE TABLE mattermost_posts (
			server_id TEXT NOT NULL CHECK (server_id <> ''),
			id TEXT NOT NULL CHECK (id <> ''),
			channel_id TEXT NOT NULL CHECK (channel_id <> ''),
			user_id TEXT NOT NULL,
			root_id TEXT NOT NULL,
			text TEXT NOT NULL,
			created_at INTEGER NOT NULL CHECK (created_at >= 0),
			updated_at INTEGER NOT NULL CHECK (updated_at >= 0),
			deleted_at INTEGER NOT NULL CHECK (deleted_at >= 0),
			reply_count INTEGER NOT NULL CHECK (reply_count >= 0),
			PRIMARY KEY (server_id, id),
			FOREIGN KEY (server_id, channel_id) REFERENCES mattermost_channels(server_id, id) ON DELETE CASCADE
		)`,
			`CREATE INDEX idx_mattermost_teams_server ON mattermost_teams(server_id, display_name, name, id)`,
			`CREATE INDEX idx_mattermost_users_server ON mattermost_users(server_id, username, id)`,
			`CREATE INDEX idx_mattermost_channels_server ON mattermost_channels(server_id, kind, display_name, name, id)`,
			`CREATE INDEX idx_mattermost_channels_team ON mattermost_channels(server_id, team_id, display_name, name, id)`,
			`CREATE INDEX idx_mattermost_posts_channel_chronology ON mattermost_posts(server_id, channel_id, root_id, created_at, id)`,
			`CREATE INDEX idx_mattermost_posts_thread ON mattermost_posts(server_id, channel_id, root_id, created_at, id)`,
			`CREATE INDEX idx_mattermost_memberships_user ON mattermost_channel_memberships(server_id, user_id, channel_id)`,
			`CREATE INDEX idx_mattermost_memberships_channel ON mattermost_channel_memberships(server_id, channel_id, user_id)`,
			`CREATE INDEX idx_mattermost_channel_users_user ON mattermost_channel_users(server_id, user_id, channel_id)`,
			`CREATE INDEX idx_mattermost_channel_users_channel ON mattermost_channel_users(server_id, channel_id, user_id)`,
		},
	},
	{
		version: 2,
		statements: []string{
			`ALTER TABLE mattermost_teams ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0 CHECK (updated_at >= 0)`,
			`DROP INDEX idx_mattermost_posts_channel_chronology`,
			`CREATE INDEX idx_mattermost_posts_channel_chronology ON mattermost_posts(server_id, channel_id, created_at, id)`,
		},
	},
}

func (db *DB) migrateMattermost(migrations []mattermostMigration) error {
	if err := validateMattermostMigrations(migrations); err != nil {
		return err
	}

	ctx := context.Background()
	conn, err := db.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring Mattermost migration connection: %w", err)
	}
	defer conn.Close()

	if err := beginImmediate(ctx, conn); err != nil {
		return fmt.Errorf("beginning Mattermost version table migration: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS cache_schema_versions (
		component TEXT PRIMARY KEY,
		version INTEGER NOT NULL CHECK (version >= 0),
		applied_at INTEGER NOT NULL CHECK (applied_at >= 0)
	)`); err != nil {
		rollbackImmediate(ctx, conn)
		return fmt.Errorf("creating cache schema versions: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		rollbackImmediate(ctx, conn)
		return fmt.Errorf("committing cache schema versions: %w", err)
	}

	if err := beginImmediate(ctx, conn); err != nil {
		return fmt.Errorf("beginning Mattermost migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackImmediate(ctx, conn)
		}
	}()

	var current int
	err = conn.QueryRowContext(ctx, `SELECT version FROM cache_schema_versions WHERE component = 'mattermost'`).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("reading Mattermost schema version: %w", err)
	}
	if current > mattermostSchemaVersion {
		return fmt.Errorf("Mattermost cache schema version %d is newer than supported version %d", current, mattermostSchemaVersion)
	}

	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		for _, statement := range migration.statements {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("applying Mattermost migration %d: %w", migration.version, err)
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO cache_schema_versions (component, version, applied_at)
			VALUES ('mattermost', ?, ?)
			ON CONFLICT(component) DO UPDATE SET version = excluded.version, applied_at = excluded.applied_at`,
			migration.version, time.Now().UnixMilli()); err != nil {
			return fmt.Errorf("recording Mattermost migration %d: %w", migration.version, err)
		}
		current = migration.version
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("committing Mattermost migration: %w", err)
	}
	committed = true
	return nil
}

func validateMattermostMigrations(migrations []mattermostMigration) error {
	if len(migrations) == 0 {
		return fmt.Errorf("Mattermost migrations must end at version %d", mattermostSchemaVersion)
	}
	if migrations[0].version != 1 {
		return errors.New("Mattermost migrations must start at version 1")
	}
	for i, migration := range migrations {
		if i > 0 && migration.version != migrations[i-1].version+1 {
			return errors.New("Mattermost migration versions must be sorted and contiguous")
		}
	}
	if migrations[len(migrations)-1].version > mattermostSchemaVersion {
		return fmt.Errorf("Mattermost migration version %d is newer than supported version %d", migrations[len(migrations)-1].version, mattermostSchemaVersion)
	}
	if migrations[len(migrations)-1].version != mattermostSchemaVersion {
		return fmt.Errorf("Mattermost migrations must end at version %d", mattermostSchemaVersion)
	}
	return nil
}

func beginImmediate(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
	return err
}

func rollbackImmediate(ctx context.Context, conn *sql.Conn) {
	_, _ = conn.ExecContext(ctx, `ROLLBACK`)
}
