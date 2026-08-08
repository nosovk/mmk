package mattermost

import (
	"reflect"
	"testing"
)

func TestChannelDoesNotContainPerUserReadState(t *testing.T) {
	if _, ok := reflect.TypeFor[Channel]().FieldByName("LastViewedAt"); ok {
		t.Fatal("Channel contains LastViewedAt; read state belongs to ChannelMembership")
	}
}

func TestChannelKindDecodesOpenChannel(t *testing.T) {
	kind, err := ParseChannelKind("O")
	if err != nil {
		t.Fatalf("ParseChannelKind returned error: %v", err)
	}
	if kind != ChannelKindPublic {
		t.Fatalf("kind = %v, want %v", kind, ChannelKindPublic)
	}
}

func TestChannelKindDecodesPrivateChannel(t *testing.T) {
	kind, err := ParseChannelKind("P")
	if err != nil {
		t.Fatalf("ParseChannelKind returned error: %v", err)
	}
	if kind != ChannelKindPrivate {
		t.Fatalf("kind = %v, want %v", kind, ChannelKindPrivate)
	}
}

func TestChannelKindDecodesDirectChannel(t *testing.T) {
	kind, err := ParseChannelKind("D")
	if err != nil {
		t.Fatalf("ParseChannelKind returned error: %v", err)
	}
	if kind != ChannelKindDirect {
		t.Fatalf("kind = %v, want %v", kind, ChannelKindDirect)
	}
}

func TestChannelKindDecodesGroupChannel(t *testing.T) {
	kind, err := ParseChannelKind("G")
	if err != nil {
		t.Fatalf("ParseChannelKind returned error: %v", err)
	}
	if kind != ChannelKindGroup {
		t.Fatalf("kind = %v, want %v", kind, ChannelKindGroup)
	}
}

func TestChannelKindRejectsUnknownCode(t *testing.T) {
	kind, err := ParseChannelKind("X")
	if err == nil {
		t.Fatal("ParseChannelKind returned nil error for an unknown code")
	}
	if kind != ChannelKindUnknown {
		t.Fatalf("kind = %v, want %v", kind, ChannelKindUnknown)
	}
}

func TestChannelKindIdentifiesDirectChannel(t *testing.T) {
	if !ChannelKindDirect.IsDirect() {
		t.Fatal("direct channel kind was not identified as direct")
	}
}

func TestChannelKindDoesNotIdentifyGroupChannelAsDirect(t *testing.T) {
	if ChannelKindGroup.IsDirect() {
		t.Fatal("group channel kind was identified as direct")
	}
}

func TestChannelKindStringReturnsPublicLabel(t *testing.T) {
	if got, want := ChannelKindPublic.String(), "public"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestChannelKindStringReturnsPrivateLabel(t *testing.T) {
	if got, want := ChannelKindPrivate.String(), "private"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestChannelKindStringReturnsDirectLabel(t *testing.T) {
	if got, want := ChannelKindDirect.String(), "direct"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestChannelKindStringReturnsGroupLabel(t *testing.T) {
	if got, want := ChannelKindGroup.String(), "group"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestChannelKindStringReturnsUnknownLabel(t *testing.T) {
	if got, want := ChannelKindUnknown.String(), "unknown"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestChannelKindStringIncludesInvalidValue(t *testing.T) {
	if got, want := ChannelKind(99).String(), "ChannelKind(99)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestUserDisplayNamePrefersNickname(t *testing.T) {
	user := User{
		ID:        "user-id",
		Username:  "mgarcia",
		Nickname:  "Mags",
		FirstName: "Maria",
		LastName:  "Garcia",
	}

	if got, want := user.DisplayName(), "Mags"; got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}
}

func TestUserDisplayNameUsesFullNameWithoutNickname(t *testing.T) {
	user := User{
		ID:        "user-id",
		Username:  "mgarcia",
		FirstName: "Maria",
		LastName:  "Garcia",
	}

	if got, want := user.DisplayName(), "Maria Garcia"; got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}
}

func TestUserDisplayNameUsesFirstNameWhenLastNameIsEmpty(t *testing.T) {
	user := User{ID: "user-id", Username: "mgarcia", FirstName: "Maria"}

	if got, want := user.DisplayName(), "Maria"; got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}
}

func TestUserDisplayNameUsesLastNameWhenFirstNameIsEmpty(t *testing.T) {
	user := User{ID: "user-id", Username: "mgarcia", LastName: "Garcia"}

	if got, want := user.DisplayName(), "Garcia"; got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}
}

func TestUserDisplayNameUsesUsernameWithoutPersonalNames(t *testing.T) {
	user := User{ID: "user-id", Username: "mgarcia"}

	if got, want := user.DisplayName(), "mgarcia"; got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}
}

func TestUserDisplayNameFallsBackToID(t *testing.T) {
	user := User{ID: "user-id"}

	if got, want := user.DisplayName(), "user-id"; got != want {
		t.Fatalf("DisplayName() = %q, want %q", got, want)
	}
}

func TestMessageThreadRootReturnsOwnIDForRootPost(t *testing.T) {
	message := Message{ID: "root-post"}

	if got, want := message.ThreadRootID(), "root-post"; got != want {
		t.Fatalf("ThreadRootID() = %q, want %q", got, want)
	}
}

func TestMessageThreadRootReturnsRootIDForReply(t *testing.T) {
	message := Message{ID: "reply-post", RootID: "root-post"}

	if got, want := message.ThreadRootID(), "root-post"; got != want {
		t.Fatalf("ThreadRootID() = %q, want %q", got, want)
	}
}

func TestConnectionStateStringReturnsConnectingLabel(t *testing.T) {
	if got, want := ConnectionStateConnecting.String(), "connecting"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestConnectionStateStringReturnsConnectedLabel(t *testing.T) {
	if got, want := ConnectionStateConnected.String(), "connected"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestConnectionStateStringReturnsOfflineLabel(t *testing.T) {
	if got, want := ConnectionStateOffline.String(), "offline"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestConnectionStateStringReturnsReconnectingLabel(t *testing.T) {
	if got, want := ConnectionStateReconnecting.String(), "reconnecting"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestConnectionStateStringIncludesInvalidValue(t *testing.T) {
	if got, want := ConnectionState(99).String(), "ConnectionState(99)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
