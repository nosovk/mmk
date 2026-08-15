// Package mattermost defines mmk's compact Mattermost application domain.
//
// The exported models in this package are not Mattermost API wire JSON DTOs.
// Transport code must decode responses into private wire types and convert
// them at the client boundary. This keeps API-specific field names and shapes,
// such as a channel's "type" code, out of the application model.
package mattermost

import (
	"fmt"
	"strings"
)

// Server identifies one configured Mattermost server and its current user.
// It is an application-domain model, not a Mattermost API wire DTO.
type Server struct {
	ID     string
	Name   string
	URL    string
	UserID string
}

// Team is a Mattermost team scoped to a configured Server.
// It is an application-domain model, not a Mattermost API wire DTO.
type Team struct {
	ID          string
	ServerID    string
	Name        string
	DisplayName string
	UpdatedAt   int64
}

// User contains the identity fields needed by mmk's display-name policy.
// It is an application-domain model, not a Mattermost API wire DTO.
type User struct {
	ID        string
	ServerID  string
	Username  string
	Nickname  string
	FirstName string
	LastName  string
	UpdatedAt int64
}

// DisplayName returns the first available name in Mattermost's usual display
// order: nickname, full name, username, then user ID.
func (u User) DisplayName() string {
	if nickname := strings.TrimSpace(u.Nickname); nickname != "" {
		return nickname
	}
	if fullName := strings.TrimSpace(strings.TrimSpace(u.FirstName) + " " + strings.TrimSpace(u.LastName)); fullName != "" {
		return fullName
	}
	if username := strings.TrimSpace(u.Username); username != "" {
		return username
	}
	return u.ID
}

// Channel is mmk's compact representation of a Mattermost channel.
//
// Kind is a domain classification, not the API's JSON "type" field. Client
// code must decode the wire type separately and convert it with
// ParseChannelKind.
type Channel struct {
	ID            string
	ServerID      string
	TeamID        string
	Name          string
	DisplayName   string
	Kind          ChannelKind
	TotalMsgCount int64
	LastPostAt    int64
	UpdatedAt     int64
	DeletedAt     int64
}

// ChannelMembership contains the per-user read metadata returned separately
// from Mattermost channel objects.
type ChannelMembership struct {
	ChannelID    string
	UserID       string
	MsgCount     int64
	MentionCount int64
	LastViewedAt int64
	UpdatedAt    int64
}

// ChannelKind classifies channels for application behavior and presentation.
type ChannelKind uint8

const (
	ChannelKindUnknown ChannelKind = iota
	ChannelKindPublic
	ChannelKindPrivate
	ChannelKindDirect
	ChannelKindGroup
)

func ParseChannelKind(code string) (ChannelKind, error) {
	switch code {
	case "O":
		return ChannelKindPublic, nil
	case "P":
		return ChannelKindPrivate, nil
	case "D":
		return ChannelKindDirect, nil
	case "G":
		return ChannelKindGroup, nil
	default:
		return ChannelKindUnknown, fmt.Errorf("unknown Mattermost channel type %q", code)
	}
}

// String returns a stable, human-readable channel classification.
func (k ChannelKind) String() string {
	switch k {
	case ChannelKindUnknown:
		return "unknown"
	case ChannelKindPublic:
		return "public"
	case ChannelKindPrivate:
		return "private"
	case ChannelKindDirect:
		return "direct"
	case ChannelKindGroup:
		return "group"
	default:
		return fmt.Sprintf("ChannelKind(%d)", k)
	}
}

func (k ChannelKind) IsDirect() bool {
	return k == ChannelKindDirect
}

// Message is mmk's compact representation of a Mattermost post.
// It is an application-domain model, not a Mattermost API wire DTO.
type Message struct {
	ID            string
	ServerID      string
	ChannelID     string
	UserID        string
	RootID        string
	Text          string
	CorrelationID string
	CreatedAt     int64
	UpdatedAt     int64
	EditedAt      int64
	DeletedAt     int64
	ReplyCount    int64
}

// ChannelPostsOptions controls one Mattermost channel-history page.
type ChannelPostsOptions struct {
	Page    int
	PerPage int
	Before  string
}

// MessagePage preserves the exact newest-first order returned by Mattermost.
type MessagePage struct {
	Messages []Message
	// OrderCount is the raw bounded order-array length before duplicate IDs
	// are collapsed. Pagination fullness is based on server-returned slots.
	OrderCount int
}

// ThreadRootID returns the root post ID for both root posts and replies.
func (m Message) ThreadRootID() string {
	if m.RootID != "" {
		return m.RootID
	}
	return m.ID
}

// ConnectionState describes a server's realtime connection lifecycle.
type ConnectionState uint8

const (
	ConnectionStateConnecting ConnectionState = iota
	ConnectionStateConnected
	ConnectionStateOffline
	ConnectionStateReconnecting
)

// String returns a stable, human-readable connection state.
func (s ConnectionState) String() string {
	switch s {
	case ConnectionStateConnecting:
		return "connecting"
	case ConnectionStateConnected:
		return "connected"
	case ConnectionStateOffline:
		return "offline"
	case ConnectionStateReconnecting:
		return "reconnecting"
	default:
		return fmt.Sprintf("ConnectionState(%d)", s)
	}
}
