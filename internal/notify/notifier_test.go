package notify

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShouldNotify_SelfMessage(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnMention:       true,
		OnDM:            true,
	}
	if ShouldNotify(ctx, "C1", "U1", "hello", "dm") {
		t.Error("should not notify for self-messages")
	}
}

func TestShouldNotify_DM(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnDM:            true,
	}
	if !ShouldNotify(ctx, "C1", "U2", "hello", "dm") {
		t.Error("should notify for DM")
	}
	if !ShouldNotify(ctx, "C1", "U2", "hello", "group_dm") {
		t.Error("should notify for group DM")
	}
}

func TestShouldNotify_DM_Disabled(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnDM:            false,
	}
	if ShouldNotify(ctx, "C1", "U2", "hello", "dm") {
		t.Error("should not notify for DM when OnDM is false")
	}
}

func TestShouldNotify_Mention(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnMention:       true,
	}
	if !ShouldNotify(ctx, "C1", "U2", "hey <@U1> check this", "channel") {
		t.Error("should notify for mention")
	}
	if ShouldNotify(ctx, "C1", "U2", "hey <@U3> check this", "channel") {
		t.Error("should not notify for mention of another user")
	}
}

func TestShouldNotify_Mention_Disabled(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnMention:       false,
	}
	if ShouldNotify(ctx, "C1", "U2", "hey <@U1> check this", "channel") {
		t.Error("should not notify for mention when OnMention is false")
	}
}

func TestShouldNotify_SpecialMentions(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnMention:       true,
	}
	if !ShouldNotify(ctx, "C1", "U2", "hey <!here> check this", "channel") {
		t.Error("should notify for @here mention")
	}
	if !ShouldNotify(ctx, "C1", "U2", "hey <!channel> check this", "channel") {
		t.Error("should notify for @channel mention")
	}
	if !ShouldNotify(ctx, "C1", "U2", "hey <!everyone> check this", "channel") {
		t.Error("should notify for @everyone mention")
	}

	ctxNoMention := ctx
	ctxNoMention.OnMention = false
	if ShouldNotify(ctxNoMention, "C1", "U2", "hey <!here> check this", "channel") {
		t.Error("should not notify for @here when OnMention is false")
	}
}

func TestShouldNotify_Keyword(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      true,
		OnKeyword:       []string{"deploy", "incident"},
	}
	if !ShouldNotify(ctx, "C1", "U2", "starting deploy now", "channel") {
		t.Error("should notify for keyword match")
	}
	if !ShouldNotify(ctx, "C1", "U2", "DEPLOY is done", "channel") {
		t.Error("should notify for case-insensitive keyword match")
	}
	if ShouldNotify(ctx, "C1", "U2", "nothing relevant", "channel") {
		t.Error("should not notify when no keyword matches")
	}
}

func TestShouldNotify_ActiveChannel_Suppressed(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C1",
		IsActiveWS:      true,
		OnDM:            true,
	}
	if ShouldNotify(ctx, "C1", "U2", "hello", "dm") {
		t.Error("should suppress notification for active channel")
	}
}

func TestShouldNotify_InactiveWorkspace_NotSuppressed(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C1",
		IsActiveWS:      false,
		OnDM:            true,
	}
	if !ShouldNotify(ctx, "C1", "U2", "hello", "dm") {
		t.Error("should notify when workspace is inactive even if channel ID matches")
	}
}

func TestShouldNotify_SuppressedByDND(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      false, // would otherwise notify
		OnDM:            true,
		OnMention:       true,
		OnKeyword:       []string{"deploy"},
		IsDND:           true,
	}
	if ShouldNotify(ctx, "C1", "U2", "hey <@U1> deploy", "dm") {
		t.Error("DND should suppress notifications regardless of triggers")
	}
}

func TestShouldNotify_SuppressedByMute(t *testing.T) {
	ctx := NotifyContext{
		CurrentUserID:   "U1",
		ActiveChannelID: "C_OTHER",
		IsActiveWS:      false, // would otherwise notify
		OnDM:            true,
		OnMention:       true,
		OnKeyword:       []string{"deploy"},
		IsMuted:         true,
	}
	if ShouldNotify(ctx, "C1", "U2", "hey <@U1> deploy", "dm") {
		t.Error("a muted conversation should suppress notifications regardless of triggers")
	}
}

func TestNotify_RunsNotifyCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	n := New(true, "printf '%s\\n%s' \"$MMK_TITLE\" \"$MMK_BODY\" >"+out)
	if err := n.Notify("the title", "the body"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading notify_command output: %v", err)
	}
	if want := "the title\nthe body"; string(got) != want {
		t.Errorf("notify_command received %q, want %q", got, want)
	}
}

func TestNotify_DisabledSkipsCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	n := New(false, "touch "+out)
	if err := n.Notify("t", "b"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("disabled notifier must not run notify_command")
	}
}

// Title/body reach the command through the environment, never interpolated into
// the command string, so a message body cannot inject a second shell command.
func TestNotify_CommandBodyIsNotInjected(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	pwned := filepath.Join(dir, "pwned")
	n := New(true, "printf '%s' \"$MMK_BODY\" >"+out)
	if err := n.Notify("title", "; touch "+pwned); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if _, err := os.Stat(pwned); !os.IsNotExist(err) {
		t.Error("message body was able to inject a shell command")
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("reading notify_command output: %v", err)
	}
	if want := "; touch " + pwned; string(got) != want {
		t.Errorf("body not passed literally: got %q, want %q", got, want)
	}
}
