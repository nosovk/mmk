package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type MattermostServer struct {
	ID            string
	Name          string
	URL           string
	CurrentUserID string
	LastSyncedAt  int64
}

type MattermostTeam struct {
	ID          string
	Name        string
	DisplayName string
	UpdatedAt   int64
	IsActive    bool
}

type MattermostUser struct {
	ID        string
	Username  string
	Nickname  string
	FirstName string
	LastName  string
	UpdatedAt int64
}

type MattermostChannel struct {
	ID            string
	TeamID        string
	Name          string
	DisplayName   string
	Kind          string
	TotalMsgCount int64
	UpdatedAt     int64
	DeletedAt     int64
	IsActive      bool
}

type MattermostChannelMembership struct {
	ChannelID    string
	UserID       string
	MsgCount     int64
	MentionCount int64
	LastViewedAt int64
	UpdatedAt    int64
}

type MattermostPost struct {
	ID         string
	ChannelID  string
	UserID     string
	RootID     string
	Text       string
	CreatedAt  int64
	UpdatedAt  int64
	DeletedAt  int64
	ReplyCount int64
}

// MattermostBootstrapSnapshot is raw cache bootstrap data, not a UI snapshot.
// ApplyMattermostBootstrapSnapshot upserts supplied rows and intentionally does
// not prune omitted rows because the v1 cache does not model authoritative
// retirement beyond channel/post deletion timestamps.
type MattermostBootstrapSnapshot struct {
	Server       MattermostServer
	CurrentUser  MattermostUser
	Users        []MattermostUser
	Teams        []MattermostTeam
	Channels     []MattermostChannel
	Memberships  []MattermostChannelMembership
	ChannelUsers map[string][]string
}

type mattermostExecer interface {
	Exec(string, ...any) (sql.Result, error)
}

func (db *DB) UpsertMattermostServer(server MattermostServer) error {
	return upsertMattermostServer(db.conn, server)
}

func upsertMattermostServer(exec mattermostExecer, server MattermostServer) error {
	if err := validateMattermostServer(server); err != nil {
		return err
	}
	// Equal revisions use lexical max per string field. This is commutative,
	// deterministic, and enriches empty values without depending on arrival order.
	_, err := exec.Exec(`INSERT INTO mattermost_servers (id, name, url, current_user_id, last_synced_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		name=CASE
			WHEN excluded.last_synced_at > mattermost_servers.last_synced_at THEN excluded.name
			WHEN excluded.last_synced_at = mattermost_servers.last_synced_at THEN max(mattermost_servers.name, excluded.name)
			ELSE mattermost_servers.name END,
		url=CASE
			WHEN excluded.last_synced_at > mattermost_servers.last_synced_at THEN excluded.url
			WHEN excluded.last_synced_at = mattermost_servers.last_synced_at THEN max(mattermost_servers.url, excluded.url)
			ELSE mattermost_servers.url END,
		current_user_id=CASE
			WHEN excluded.last_synced_at > mattermost_servers.last_synced_at THEN excluded.current_user_id
			WHEN excluded.last_synced_at = mattermost_servers.last_synced_at THEN max(mattermost_servers.current_user_id, excluded.current_user_id)
			ELSE mattermost_servers.current_user_id END,
		last_synced_at=max(mattermost_servers.last_synced_at, excluded.last_synced_at)`,
		server.ID, server.Name, server.URL, server.CurrentUserID, server.LastSyncedAt)
	return wrapMattermostError("upserting server", err)
}

func (db *DB) GetMattermostServer(serverID string) (MattermostServer, error) {
	var server MattermostServer
	err := db.conn.QueryRow(`SELECT id, name, url, current_user_id, last_synced_at FROM mattermost_servers WHERE id = ?`, serverID).
		Scan(&server.ID, &server.Name, &server.URL, &server.CurrentUserID, &server.LastSyncedAt)
	return server, wrapMattermostError("getting server", err)
}

func (db *DB) ListMattermostServers() ([]MattermostServer, error) {
	rows, err := db.conn.Query(`SELECT id, name, url, current_user_id, last_synced_at FROM mattermost_servers ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost servers: %w", err)
	}
	defer rows.Close()
	servers := []MattermostServer{}
	for rows.Next() {
		var server MattermostServer
		if err := rows.Scan(&server.ID, &server.Name, &server.URL, &server.CurrentUserID, &server.LastSyncedAt); err != nil {
			return nil, fmt.Errorf("scanning Mattermost server: %w", err)
		}
		servers = append(servers, server)
	}
	return servers, rows.Err()
}

func (db *DB) DeleteMattermostServer(serverID string) error {
	if err := requireMattermostID("server", serverID); err != nil {
		return err
	}
	_, err := db.conn.Exec(`DELETE FROM mattermost_servers WHERE id = ?`, serverID)
	return wrapMattermostError("deleting server", err)
}

func (db *DB) UpsertMattermostTeam(serverID string, team MattermostTeam) error {
	return upsertMattermostTeam(db.conn, serverID, team)
}

func upsertMattermostTeam(exec mattermostExecer, serverID string, team MattermostTeam) error {
	if err := requireMattermostIDs(serverID, team.ID); err != nil {
		return err
	}
	if team.UpdatedAt < 0 {
		return errors.New("Mattermost team updated_at must not be negative")
	}
	_, err := exec.Exec(`INSERT INTO mattermost_teams (server_id, id, name, display_name, updated_at, is_active) VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT(server_id, id) DO UPDATE SET
		name=CASE WHEN excluded.updated_at > mattermost_teams.updated_at THEN excluded.name
			WHEN excluded.updated_at = mattermost_teams.updated_at THEN max(mattermost_teams.name, excluded.name)
			ELSE mattermost_teams.name END,
		display_name=CASE WHEN excluded.updated_at > mattermost_teams.updated_at THEN excluded.display_name
			WHEN excluded.updated_at = mattermost_teams.updated_at THEN max(mattermost_teams.display_name, excluded.display_name)
			ELSE mattermost_teams.display_name END,
		updated_at=max(mattermost_teams.updated_at, excluded.updated_at)`,
		serverID, team.ID, team.Name, team.DisplayName, team.UpdatedAt)
	return wrapMattermostError("upserting team", err)
}

func (db *DB) GetMattermostTeam(serverID, teamID string) (MattermostTeam, error) {
	var team MattermostTeam
	err := db.conn.QueryRow(`SELECT id, name, display_name, updated_at FROM mattermost_teams WHERE server_id = ? AND id = ?`, serverID, teamID).
		Scan(&team.ID, &team.Name, &team.DisplayName, &team.UpdatedAt)
	return team, wrapMattermostError("getting team", err)
}

func (db *DB) ListMattermostTeams(serverID string) ([]MattermostTeam, error) {
	rows, err := db.conn.Query(`SELECT id, name, display_name, updated_at FROM mattermost_teams WHERE server_id = ? AND is_active = 1 ORDER BY display_name, name, id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost teams: %w", err)
	}
	defer rows.Close()
	teams := []MattermostTeam{}
	for rows.Next() {
		var team MattermostTeam
		if err := rows.Scan(&team.ID, &team.Name, &team.DisplayName, &team.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning Mattermost team: %w", err)
		}
		teams = append(teams, team)
	}
	return teams, rows.Err()
}

func (db *DB) UpsertMattermostUser(serverID string, user MattermostUser) error {
	return upsertMattermostUser(db.conn, serverID, user)
}

func upsertMattermostUser(exec mattermostExecer, serverID string, user MattermostUser) error {
	if err := requireMattermostIDs(serverID, user.ID); err != nil {
		return err
	}
	if user.UpdatedAt < 0 {
		return errors.New("Mattermost user updated_at must not be negative")
	}
	_, err := exec.Exec(`INSERT INTO mattermost_users
		(server_id, id, username, nickname, first_name, last_name, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id, id) DO UPDATE SET
		username=CASE WHEN excluded.updated_at > mattermost_users.updated_at THEN excluded.username
			WHEN excluded.updated_at = mattermost_users.updated_at THEN max(mattermost_users.username, excluded.username)
			ELSE mattermost_users.username END,
		nickname=CASE WHEN excluded.updated_at > mattermost_users.updated_at THEN excluded.nickname
			WHEN excluded.updated_at = mattermost_users.updated_at THEN max(mattermost_users.nickname, excluded.nickname)
			ELSE mattermost_users.nickname END,
		first_name=CASE WHEN excluded.updated_at > mattermost_users.updated_at THEN excluded.first_name
			WHEN excluded.updated_at = mattermost_users.updated_at THEN max(mattermost_users.first_name, excluded.first_name)
			ELSE mattermost_users.first_name END,
		last_name=CASE WHEN excluded.updated_at > mattermost_users.updated_at THEN excluded.last_name
			WHEN excluded.updated_at = mattermost_users.updated_at THEN max(mattermost_users.last_name, excluded.last_name)
			ELSE mattermost_users.last_name END,
		updated_at=max(mattermost_users.updated_at, excluded.updated_at)`,
		serverID, user.ID, user.Username, user.Nickname, user.FirstName, user.LastName, user.UpdatedAt)
	return wrapMattermostError("upserting user", err)
}

func (db *DB) GetMattermostUser(serverID, userID string) (MattermostUser, error) {
	var user MattermostUser
	err := db.conn.QueryRow(`SELECT id, username, nickname, first_name, last_name, updated_at
		FROM mattermost_users WHERE server_id = ? AND id = ?`, serverID, userID).
		Scan(&user.ID, &user.Username, &user.Nickname, &user.FirstName, &user.LastName, &user.UpdatedAt)
	return user, wrapMattermostError("getting user", err)
}

func (db *DB) ListMattermostUsers(serverID string) ([]MattermostUser, error) {
	rows, err := db.conn.Query(`SELECT id, username, nickname, first_name, last_name, updated_at
		FROM mattermost_users WHERE server_id = ? ORDER BY username, id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost users: %w", err)
	}
	defer rows.Close()
	users := []MattermostUser{}
	for rows.Next() {
		var user MattermostUser
		if err := rows.Scan(&user.ID, &user.Username, &user.Nickname, &user.FirstName, &user.LastName, &user.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning Mattermost user: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (db *DB) UpsertMattermostChannel(serverID string, channel MattermostChannel) error {
	return upsertMattermostChannel(db.conn, serverID, channel)
}

func upsertMattermostChannel(exec mattermostExecer, serverID string, channel MattermostChannel) error {
	if err := validateMattermostChannel(serverID, channel); err != nil {
		return err
	}
	var teamID any
	if channel.TeamID != "" {
		teamID = channel.TeamID
	}
	_, err := exec.Exec(`INSERT INTO mattermost_channels
		(server_id, id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at, is_active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT(server_id, id) DO UPDATE SET
		team_id=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at)
				THEN coalesce(excluded.team_id, mattermost_channels.team_id)
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at)
				THEN CASE WHEN coalesce(excluded.team_id, '') > coalesce(mattermost_channels.team_id, '') THEN excluded.team_id ELSE mattermost_channels.team_id END
			ELSE mattermost_channels.team_id END,
		name=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at)
				THEN CASE WHEN excluded.name <> '' THEN excluded.name ELSE mattermost_channels.name END
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN max(mattermost_channels.name, excluded.name)
			ELSE mattermost_channels.name END,
		display_name=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at)
				THEN CASE WHEN excluded.display_name <> '' THEN excluded.display_name ELSE mattermost_channels.display_name END
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN max(mattermost_channels.display_name, excluded.display_name)
			ELSE mattermost_channels.display_name END,
		kind=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at)
				THEN CASE
					WHEN excluded.deleted_at > excluded.updated_at AND excluded.name = '' AND excluded.display_name = '' AND excluded.team_id IS NULL THEN mattermost_channels.kind
					ELSE excluded.kind END
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN max(mattermost_channels.kind, excluded.kind)
			ELSE mattermost_channels.kind END,
		total_msg_count=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN excluded.total_msg_count
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN max(mattermost_channels.total_msg_count, excluded.total_msg_count)
			ELSE mattermost_channels.total_msg_count END,
		updated_at=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN excluded.updated_at
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN max(mattermost_channels.updated_at, excluded.updated_at)
			ELSE mattermost_channels.updated_at END,
		deleted_at=CASE
			WHEN max(excluded.updated_at, excluded.deleted_at) > max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN excluded.deleted_at
			WHEN max(excluded.updated_at, excluded.deleted_at) = max(mattermost_channels.updated_at, mattermost_channels.deleted_at) THEN max(mattermost_channels.deleted_at, excluded.deleted_at)
			ELSE mattermost_channels.deleted_at END`,
		serverID, channel.ID, teamID, channel.Name, channel.DisplayName, channel.Kind,
		channel.TotalMsgCount, channel.UpdatedAt, channel.DeletedAt)
	return wrapMattermostError("upserting channel", err)
}

func (db *DB) GetMattermostChannel(serverID, channelID string) (MattermostChannel, error) {
	var channel MattermostChannel
	var teamID sql.NullString
	err := db.conn.QueryRow(`SELECT id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at
		FROM mattermost_channels WHERE server_id = ? AND id = ?`, serverID, channelID).
		Scan(&channel.ID, &teamID, &channel.Name, &channel.DisplayName, &channel.Kind,
			&channel.TotalMsgCount, &channel.UpdatedAt, &channel.DeletedAt)
	channel.TeamID = teamID.String
	return channel, wrapMattermostError("getting channel", err)
}

func (db *DB) ListMattermostChannels(serverID string) ([]MattermostChannel, error) {
	return db.listMattermostChannels(`SELECT id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at
		FROM mattermost_channels WHERE server_id = ? ORDER BY display_name, name, id`, serverID)
}

func (db *DB) ListMattermostTeamChannels(serverID, teamID string) ([]MattermostChannel, error) {
	return db.listMattermostChannels(`SELECT id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at
		FROM mattermost_channels WHERE server_id = ? AND team_id = ? ORDER BY display_name, name, id`, serverID, teamID)
}

func (db *DB) listMattermostChannels(query string, args ...any) ([]MattermostChannel, error) {
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost channels: %w", err)
	}
	defer rows.Close()
	channels := []MattermostChannel{}
	for rows.Next() {
		var channel MattermostChannel
		var teamID sql.NullString
		if err := rows.Scan(&channel.ID, &teamID, &channel.Name, &channel.DisplayName, &channel.Kind,
			&channel.TotalMsgCount, &channel.UpdatedAt, &channel.DeletedAt); err != nil {
			return nil, fmt.Errorf("scanning Mattermost channel: %w", err)
		}
		channel.TeamID = teamID.String
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func (db *DB) UpsertMattermostChannelMembership(serverID string, membership MattermostChannelMembership) error {
	return upsertMattermostChannelMembership(db.conn, serverID, membership)
}

func upsertMattermostChannelMembership(exec mattermostExecer, serverID string, membership MattermostChannelMembership) error {
	if err := requireMattermostIDs(serverID, membership.ChannelID, membership.UserID); err != nil {
		return err
	}
	if min(membership.MsgCount, membership.MentionCount, membership.LastViewedAt, membership.UpdatedAt) < 0 {
		return errors.New("Mattermost membership counts and timestamps must not be negative")
	}
	// Equal revisions merge counters/view time by max. In particular, revision
	// zero remains useful for initial bootstrap rows and deterministic replays.
	_, err := exec.Exec(`INSERT INTO mattermost_channel_memberships
		(server_id, channel_id, user_id, msg_count, mention_count, last_viewed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id, channel_id, user_id) DO UPDATE SET
		msg_count=CASE WHEN excluded.updated_at > mattermost_channel_memberships.updated_at THEN excluded.msg_count
			WHEN excluded.updated_at = mattermost_channel_memberships.updated_at THEN max(mattermost_channel_memberships.msg_count, excluded.msg_count)
			ELSE mattermost_channel_memberships.msg_count END,
		mention_count=CASE WHEN excluded.updated_at > mattermost_channel_memberships.updated_at THEN excluded.mention_count
			WHEN excluded.updated_at = mattermost_channel_memberships.updated_at THEN max(mattermost_channel_memberships.mention_count, excluded.mention_count)
			ELSE mattermost_channel_memberships.mention_count END,
		last_viewed_at=CASE WHEN excluded.updated_at > mattermost_channel_memberships.updated_at THEN excluded.last_viewed_at
			WHEN excluded.updated_at = mattermost_channel_memberships.updated_at THEN max(mattermost_channel_memberships.last_viewed_at, excluded.last_viewed_at)
			ELSE mattermost_channel_memberships.last_viewed_at END,
		updated_at=max(mattermost_channel_memberships.updated_at, excluded.updated_at)`,
		serverID, membership.ChannelID, membership.UserID, membership.MsgCount, membership.MentionCount,
		membership.LastViewedAt, membership.UpdatedAt)
	return wrapMattermostError("upserting channel membership", err)
}

func (db *DB) GetMattermostChannelMembership(serverID, channelID, userID string) (MattermostChannelMembership, error) {
	var membership MattermostChannelMembership
	err := db.conn.QueryRow(`SELECT channel_id, user_id, msg_count, mention_count, last_viewed_at, updated_at
		FROM mattermost_channel_memberships WHERE server_id = ? AND channel_id = ? AND user_id = ?`, serverID, channelID, userID).
		Scan(&membership.ChannelID, &membership.UserID, &membership.MsgCount, &membership.MentionCount,
			&membership.LastViewedAt, &membership.UpdatedAt)
	return membership, wrapMattermostError("getting channel membership", err)
}

func (db *DB) ListMattermostChannelMemberships(serverID, userID string) ([]MattermostChannelMembership, error) {
	rows, err := db.conn.Query(`SELECT channel_id, user_id, msg_count, mention_count, last_viewed_at, updated_at
		FROM mattermost_channel_memberships WHERE server_id = ? AND user_id = ? ORDER BY channel_id`, serverID, userID)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost channel memberships: %w", err)
	}
	defer rows.Close()
	memberships := []MattermostChannelMembership{}
	for rows.Next() {
		var membership MattermostChannelMembership
		if err := rows.Scan(&membership.ChannelID, &membership.UserID, &membership.MsgCount, &membership.MentionCount,
			&membership.LastViewedAt, &membership.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning Mattermost channel membership: %w", err)
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

func (db *DB) ReplaceMattermostChannelUserIDs(serverID, channelID string, userIDs []string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("beginning Mattermost participant replacement: %w", err)
	}
	if err := replaceMattermostChannelUserIDs(tx, serverID, channelID, userIDs); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func replaceMattermostChannelUserIDs(exec mattermostExecer, serverID, channelID string, userIDs []string) error {
	if err := requireMattermostIDs(serverID, channelID); err != nil {
		return err
	}
	if _, err := exec.Exec(`DELETE FROM mattermost_channel_users WHERE server_id = ? AND channel_id = ?`, serverID, channelID); err != nil {
		return fmt.Errorf("clearing Mattermost channel participants: %w", err)
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if err := requireMattermostID("user", userID); err != nil {
			return err
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		if _, err := exec.Exec(`INSERT INTO mattermost_channel_users (server_id, channel_id, user_id) VALUES (?, ?, ?)`, serverID, channelID, userID); err != nil {
			return fmt.Errorf("inserting Mattermost channel participant: %w", err)
		}
	}
	return nil
}

func upsertMattermostChannelUserIDs(exec mattermostExecer, serverID, channelID string, userIDs []string) error {
	if err := requireMattermostIDs(serverID, channelID); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if err := requireMattermostID("user", userID); err != nil {
			return err
		}
		if _, exists := seen[userID]; exists {
			continue
		}
		seen[userID] = struct{}{}
		if _, err := exec.Exec(`INSERT INTO mattermost_channel_users (server_id, channel_id, user_id) VALUES (?, ?, ?)
			ON CONFLICT(server_id, channel_id, user_id) DO NOTHING`, serverID, channelID, userID); err != nil {
			return fmt.Errorf("upserting Mattermost channel participant: %w", err)
		}
	}
	return nil
}

func (db *DB) ListMattermostChannelUserIDs(serverID, channelID string) ([]string, error) {
	rows, err := db.conn.Query(`SELECT user_id FROM mattermost_channel_users WHERE server_id = ? AND channel_id = ? ORDER BY user_id`, serverID, channelID)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost channel participants: %w", err)
	}
	defer rows.Close()
	userIDs := []string{}
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			return nil, fmt.Errorf("scanning Mattermost channel participant: %w", err)
		}
		userIDs = append(userIDs, userID)
	}
	return userIDs, rows.Err()
}

func (db *DB) ApplyMattermostBootstrapSnapshot(snapshot MattermostBootstrapSnapshot) error {
	return db.applyMattermostBootstrapSnapshot(snapshot, false)
}

func (db *DB) ReplaceMattermostBootstrapSnapshot(snapshot MattermostBootstrapSnapshot) error {
	return db.applyMattermostBootstrapSnapshot(snapshot, true)
}

func (db *DB) applyMattermostBootstrapSnapshot(snapshot MattermostBootstrapSnapshot, replace bool) error {
	serverID := snapshot.Server.ID
	if snapshot.CurrentUser.ID != snapshot.Server.CurrentUserID {
		return errors.New("Mattermost snapshot current user must match server current_user_id")
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("beginning Mattermost snapshot: %w", err)
	}
	fail := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if err := upsertMattermostServer(tx, snapshot.Server); err != nil {
		return fail(err)
	}
	for _, user := range snapshot.Users {
		if err := upsertMattermostUser(tx, serverID, user); err != nil {
			return fail(err)
		}
	}
	if err := upsertMattermostUser(tx, serverID, snapshot.CurrentUser); err != nil {
		return fail(err)
	}
	for _, team := range snapshot.Teams {
		if err := upsertMattermostTeam(tx, serverID, team); err != nil {
			return fail(err)
		}
	}
	for _, channel := range snapshot.Channels {
		if err := upsertMattermostChannel(tx, serverID, channel); err != nil {
			return fail(err)
		}
	}
	for _, membership := range snapshot.Memberships {
		if err := upsertMattermostChannelMembership(tx, serverID, membership); err != nil {
			return fail(err)
		}
	}
	channelIDs := make([]string, 0, len(snapshot.ChannelUsers))
	for channelID := range snapshot.ChannelUsers {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Strings(channelIDs)
	for _, channelID := range channelIDs {
		var err error
		if replace {
			err = replaceMattermostChannelUserIDs(tx, serverID, channelID, snapshot.ChannelUsers[channelID])
		} else {
			err = upsertMattermostChannelUserIDs(tx, serverID, channelID, snapshot.ChannelUsers[channelID])
		}
		if err != nil {
			return fail(err)
		}
	}
	if replace {
		for _, channel := range snapshot.Channels {
			if _, ok := snapshot.ChannelUsers[channel.ID]; ok {
				continue
			}
			if err := replaceMattermostChannelUserIDs(tx, serverID, channel.ID, nil); err != nil {
				return fail(err)
			}
		}
		if _, err := tx.Exec(`UPDATE mattermost_teams SET is_active = CASE WHEN id IN (`+placeholders(len(snapshot.Teams))+`) THEN 1 ELSE 0 END WHERE server_id = ?`, append(teamIDArgs(snapshot.Teams), serverID)...); err != nil {
			return fail(err)
		}
		if _, err := tx.Exec(`UPDATE mattermost_channels SET is_active = CASE WHEN id IN (`+placeholders(len(snapshot.Channels))+`) THEN 1 ELSE 0 END WHERE server_id = ?`, append(channelIDArgs(snapshot.Channels), serverID)...); err != nil {
			return fail(err)
		}
		if len(snapshot.Memberships) == 0 {
			if _, err := tx.Exec(`DELETE FROM mattermost_channel_memberships WHERE server_id = ? AND user_id = ?`, serverID, snapshot.Server.CurrentUserID); err != nil {
				return fail(err)
			}
		} else if _, err := tx.Exec(`DELETE FROM mattermost_channel_memberships WHERE server_id = ? AND user_id = ? AND channel_id NOT IN (`+placeholders(len(snapshot.Memberships))+`)`, append([]any{serverID, snapshot.Server.CurrentUserID}, membershipIDArgs(snapshot.Memberships)...)...); err != nil {
			return fail(err)
		}
		if _, err := tx.Exec(`DELETE FROM mattermost_channel_users WHERE server_id = ? AND channel_id NOT IN (`+placeholders(len(snapshot.Channels))+`)`, append([]any{serverID}, channelIDArgs(snapshot.Channels)...)...); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing Mattermost snapshot: %w", err)
	}
	return nil
}

func (db *DB) LoadMattermostBootstrapSnapshot(serverID string) (MattermostBootstrapSnapshot, error) {
	server, err := db.GetMattermostServer(serverID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	currentUser, err := db.GetMattermostUser(serverID, server.CurrentUserID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	users, err := db.ListMattermostUsers(serverID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	teams, err := db.ListMattermostTeams(serverID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	channels, err := db.listMattermostChannels(`SELECT id, team_id, name, display_name, kind, total_msg_count, updated_at, deleted_at
		FROM mattermost_channels WHERE server_id = ? AND deleted_at = 0 AND is_active = 1 ORDER BY display_name, name, id`, serverID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	memberships, err := db.listActiveMattermostChannelMemberships(serverID, server.CurrentUserID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	channelUsers, err := db.listMattermostServerChannelUserIDs(serverID)
	if err != nil {
		return MattermostBootstrapSnapshot{}, err
	}
	return MattermostBootstrapSnapshot{
		Server: server, CurrentUser: currentUser, Users: users, Teams: teams,
		Channels: channels, Memberships: memberships, ChannelUsers: channelUsers,
	}, nil
}

func (db *DB) listActiveMattermostChannelMemberships(serverID, userID string) ([]MattermostChannelMembership, error) {
	rows, err := db.conn.Query(`SELECT m.channel_id, m.user_id, m.msg_count, m.mention_count, m.last_viewed_at, m.updated_at FROM mattermost_channel_memberships m JOIN mattermost_channels c ON c.server_id=m.server_id AND c.id=m.channel_id WHERE m.server_id=? AND m.user_id=? AND c.is_active=1 AND c.deleted_at=0 ORDER BY m.channel_id`, serverID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MattermostChannelMembership
	for rows.Next() {
		var m MattermostChannelMembership
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.MsgCount, &m.MentionCount, &m.LastViewedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (db *DB) listMattermostServerChannelUserIDs(serverID string) (map[string][]string, error) {
	rows, err := db.conn.Query(`SELECT cu.channel_id, cu.user_id FROM mattermost_channel_users cu JOIN mattermost_channels c ON c.server_id=cu.server_id AND c.id=cu.channel_id WHERE cu.server_id=? AND c.is_active=1 AND c.deleted_at=0 ORDER BY cu.channel_id, cu.user_id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("listing Mattermost server participants: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var channelID, userID string
		if err := rows.Scan(&channelID, &userID); err != nil {
			return nil, err
		}
		out[channelID] = append(out[channelID], userID)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n == 0 {
		return "NULL"
	}
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}
func teamIDArgs(items []MattermostTeam) []any {
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i].ID
	}
	return out
}
func channelIDArgs(items []MattermostChannel) []any {
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i].ID
	}
	return out
}
func membershipIDArgs(items []MattermostChannelMembership) []any {
	out := make([]any, len(items))
	for i := range items {
		out[i] = items[i].ChannelID
	}
	return out
}

func (db *DB) UpsertMattermostPost(serverID string, post MattermostPost) error {
	return upsertMattermostPost(db.conn, serverID, post)
}

func upsertMattermostPost(exec mattermostExecer, serverID string, post MattermostPost) error {
	if err := validateMattermostPost(serverID, post); err != nil {
		return err
	}
	// Keep the merge inside one SQLite statement so concurrent writers serialize
	// on the row. Newer revisions update supplied fields; equal revisions merge
	// commutatively, with max timestamps/counters and deletion dominance.
	_, err := exec.Exec(`INSERT INTO mattermost_posts
		(server_id, id, channel_id, user_id, root_id, text, created_at, updated_at, deleted_at, reply_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id, id) DO UPDATE SET
		channel_id=CASE
			WHEN max(excluded.created_at, excluded.updated_at, excluded.deleted_at) > max(mattermost_posts.created_at, mattermost_posts.updated_at, mattermost_posts.deleted_at)
				THEN CASE WHEN excluded.channel_id <> '' THEN excluded.channel_id ELSE mattermost_posts.channel_id END
			ELSE max(mattermost_posts.channel_id, excluded.channel_id) END,
		user_id=CASE
			WHEN max(excluded.created_at, excluded.updated_at, excluded.deleted_at) > max(mattermost_posts.created_at, mattermost_posts.updated_at, mattermost_posts.deleted_at)
				THEN CASE WHEN excluded.user_id <> '' THEN excluded.user_id ELSE mattermost_posts.user_id END
			ELSE max(mattermost_posts.user_id, excluded.user_id) END,
		root_id=CASE
			WHEN max(excluded.created_at, excluded.updated_at, excluded.deleted_at) > max(mattermost_posts.created_at, mattermost_posts.updated_at, mattermost_posts.deleted_at)
				THEN CASE WHEN excluded.root_id <> '' THEN excluded.root_id ELSE mattermost_posts.root_id END
			ELSE max(mattermost_posts.root_id, excluded.root_id) END,
		text=CASE
			WHEN max(excluded.created_at, excluded.updated_at, excluded.deleted_at) > max(mattermost_posts.created_at, mattermost_posts.updated_at, mattermost_posts.deleted_at)
				THEN CASE WHEN excluded.text <> '' THEN excluded.text ELSE mattermost_posts.text END
			ELSE max(mattermost_posts.text, excluded.text) END,
		created_at=max(mattermost_posts.created_at, excluded.created_at),
		updated_at=max(mattermost_posts.updated_at, excluded.updated_at),
		deleted_at=max(mattermost_posts.deleted_at, excluded.deleted_at),
		reply_count=CASE
			WHEN max(excluded.created_at, excluded.updated_at, excluded.deleted_at) > max(mattermost_posts.created_at, mattermost_posts.updated_at, mattermost_posts.deleted_at) THEN excluded.reply_count
			ELSE max(mattermost_posts.reply_count, excluded.reply_count) END
		WHERE max(excluded.created_at, excluded.updated_at, excluded.deleted_at) >=
		      max(mattermost_posts.created_at, mattermost_posts.updated_at, mattermost_posts.deleted_at)`,
		serverID, post.ID, post.ChannelID, post.UserID, post.RootID, post.Text,
		post.CreatedAt, post.UpdatedAt, post.DeletedAt, post.ReplyCount)
	return wrapMattermostError("upserting post", err)
}

func (db *DB) UpsertMattermostPosts(serverID string, posts []MattermostPost) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("beginning Mattermost post upsert: %w", err)
	}
	for _, post := range posts {
		if err := upsertMattermostPost(tx, serverID, post); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing Mattermost post upsert: %w", err)
	}
	return nil
}

func (db *DB) GetMattermostPost(serverID, postID string) (MattermostPost, error) {
	var post MattermostPost
	err := db.conn.QueryRow(`SELECT id, channel_id, user_id, root_id, text, created_at, updated_at, deleted_at, reply_count
		FROM mattermost_posts WHERE server_id = ? AND id = ?`, serverID, postID).
		Scan(&post.ID, &post.ChannelID, &post.UserID, &post.RootID, &post.Text,
			&post.CreatedAt, &post.UpdatedAt, &post.DeletedAt, &post.ReplyCount)
	return post, wrapMattermostError("getting post", err)
}

func (db *DB) ListMattermostChannelPosts(serverID, channelID string, limit int, beforePostID string) ([]MattermostPost, error) {
	if err := requireMattermostIDs(serverID, channelID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, errors.New("Mattermost post limit must be positive")
	}
	inner := `SELECT id, channel_id, user_id, root_id, text, created_at, updated_at, deleted_at, reply_count
		FROM mattermost_posts WHERE server_id = ? AND channel_id = ? AND root_id = '' AND deleted_at = 0`
	args := []any{serverID, channelID}
	if beforePostID != "" {
		var createdAt int64
		var id string
		err := db.conn.QueryRow(`SELECT created_at, id FROM mattermost_posts
			WHERE server_id = ? AND channel_id = ? AND id = ? AND root_id = '' AND deleted_at = 0`,
			serverID, channelID, beforePostID).Scan(&createdAt, &id)
		if err != nil {
			return nil, wrapMattermostError("resolving post anchor", err)
		}
		inner += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, createdAt, createdAt, id)
	}
	inner += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	return db.queryMattermostPosts(`SELECT * FROM (`+inner+`) ORDER BY created_at, id`, args...)
}

func (db *DB) ListMattermostThreadPosts(serverID, channelID, rootPostID string) ([]MattermostPost, error) {
	if err := requireMattermostIDs(serverID, channelID, rootPostID); err != nil {
		return nil, err
	}
	return db.queryMattermostPosts(`SELECT id, channel_id, user_id, root_id, text, created_at, updated_at, deleted_at, reply_count
		FROM mattermost_posts WHERE server_id = ? AND channel_id = ? AND deleted_at = 0 AND (id = ? OR root_id = ?)
		ORDER BY created_at, id`, serverID, channelID, rootPostID, rootPostID)
}

func (db *DB) MarkMattermostPostDeleted(serverID, postID string, deletedAt int64) error {
	if err := requireMattermostIDs(serverID, postID); err != nil {
		return err
	}
	if deletedAt < 0 {
		return errors.New("Mattermost post deleted_at must not be negative")
	}
	result, err := db.conn.Exec(`UPDATE mattermost_posts SET deleted_at = max(deleted_at, ?)
		WHERE server_id = ? AND id = ? AND ? >= max(created_at, updated_at, deleted_at)`, deletedAt, serverID, postID, deletedAt)
	if err != nil {
		return fmt.Errorf("marking Mattermost post deleted: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking Mattermost post deletion: %w", err)
	}
	if changed == 0 {
		var exists int
		if err := db.conn.QueryRow(`SELECT COUNT(*) FROM mattermost_posts WHERE server_id = ? AND id = ?`, serverID, postID).Scan(&exists); err != nil {
			return fmt.Errorf("checking Mattermost post existence: %w", err)
		}
		if exists == 0 {
			return sql.ErrNoRows
		}
	}
	return nil
}

func (db *DB) queryMattermostPosts(query string, args ...any) ([]MattermostPost, error) {
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying Mattermost posts: %w", err)
	}
	defer rows.Close()
	posts := []MattermostPost{}
	for rows.Next() {
		var post MattermostPost
		if err := rows.Scan(&post.ID, &post.ChannelID, &post.UserID, &post.RootID, &post.Text,
			&post.CreatedAt, &post.UpdatedAt, &post.DeletedAt, &post.ReplyCount); err != nil {
			return nil, fmt.Errorf("scanning Mattermost post: %w", err)
		}
		posts = append(posts, post)
	}
	return posts, rows.Err()
}

func validateMattermostServer(server MattermostServer) error {
	if err := requireMattermostIDs(server.ID, server.CurrentUserID); err != nil {
		return err
	}
	if strings.TrimSpace(server.URL) == "" {
		return errors.New("Mattermost server URL must not be empty")
	}
	if server.LastSyncedAt < 0 {
		return errors.New("Mattermost server last_synced_at must not be negative")
	}
	return nil
}

func validateMattermostChannel(serverID string, channel MattermostChannel) error {
	if err := requireMattermostIDs(serverID, channel.ID); err != nil {
		return err
	}
	switch channel.Kind {
	case "public", "private":
		if channel.TeamID == "" {
			return errors.New("Mattermost public/private channel team ID must not be empty")
		}
	case "direct", "group":
	default:
		return fmt.Errorf("invalid Mattermost channel kind %q", channel.Kind)
	}
	if min(channel.TotalMsgCount, channel.UpdatedAt, channel.DeletedAt) < 0 {
		return errors.New("Mattermost channel counts and timestamps must not be negative")
	}
	return nil
}

func validateMattermostPost(serverID string, post MattermostPost) error {
	if err := requireMattermostIDs(serverID, post.ID, post.ChannelID); err != nil {
		return err
	}
	if min(post.CreatedAt, post.UpdatedAt, post.DeletedAt, post.ReplyCount) < 0 {
		return errors.New("Mattermost post counts and timestamps must not be negative")
	}
	return nil
}

func requireMattermostIDs(ids ...string) error {
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errors.New("Mattermost ID must not be empty")
		}
	}
	return nil
}

func requireMattermostID(kind, id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("Mattermost %s ID must not be empty", kind)
	}
	return nil
}

func wrapMattermostError(operation string, err error) error {
	if err == nil || errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return fmt.Errorf("%s Mattermost cache record: %w", operation, err)
}
