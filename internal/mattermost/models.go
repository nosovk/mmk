package mattermost

import (
	"fmt"
	"strings"
)

type Server struct {
	ID     string
	Name   string
	URL    string
	UserID string
}

type Team struct {
	ID          string
	ServerID    string
	Name        string
	DisplayName string
}

type User struct {
	ID        string
	ServerID  string
	Username  string
	Nickname  string
	FirstName string
	LastName  string
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

type Channel struct {
	ID           string
	ServerID     string
	TeamID       string
	Name         string
	DisplayName  string
	Kind         ChannelKind
	LastViewedAt int64
}

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

func (k ChannelKind) IsDirect() bool {
	return k == ChannelKindDirect
}

type Message struct {
	ID        string
	ChannelID string
	UserID    string
	RootID    string
	Text      string
	CreatedAt int64
	UpdatedAt int64
}

// ThreadRootID returns the root post ID for both root posts and replies.
func (m Message) ThreadRootID() string {
	if m.RootID != "" {
		return m.RootID
	}
	return m.ID
}

type ConnectionState uint8

const (
	ConnectionStateConnecting ConnectionState = iota
	ConnectionStateConnected
	ConnectionStateOffline
	ConnectionStateReconnecting
)
