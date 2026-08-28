package ui

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/statusbar"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

type recordingMattermostSendService struct {
	contexts []context.Context
	requests []MattermostSendRequest
	result   tea.Msg
}

func (s *recordingMattermostSendService) Send(ctx context.Context, request MattermostSendRequest) tea.Msg {
	s.contexts = append(s.contexts, ctx)
	s.requests = append(s.requests, request)
	return s.result
}

func newMattermostSendApp(t *testing.T, service MattermostSendService) *App {
	t.Helper()
	a := NewApp()
	a.width = 200
	a.height = 50
	a.SetMattermostSendService(service)
	_, cmd := a.Update(ServerReadyMsg{Server: ServerViewState{
		ServerID:      ids.ServerID("server-1"),
		InitialActive: true,
		UserID:        "user-1",
		UserNames:     map[string]string{"user-1": "you"},
		Channels:      testMattermostChannels(),
	}})
	if cmd == nil {
		t.Fatal("server activation did not select the initial channel")
	}
	selected := cmd()
	if _, ok := selected.(ChannelSelectedMsg); !ok {
		t.Fatalf("activation command returned %T, want ChannelSelectedMsg", selected)
	}
	_, _ = a.Update(selected)
	return a
}

func typeInMattermostCompose(t *testing.T, a *App, text string) {
	t.Helper()
	for _, r := range text {
		_ = a.handleInsertMode(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestMattermostMentionSelectionSendsOrdinaryUserIDAtWordBoundary(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := NewApp()
	a.width = 200
	a.height = 50
	a.SetMattermostSendService(service)
	_, activate := a.Update(ServerReadyMsg{Server: ServerViewState{
		ServerID:      ids.ServerID("server-1"),
		InitialActive: true,
		UserID:        "user-1",
		UserNames: map[string]string{
			"user-1": "you",
			"user-2": "Alice",
		},
		Channels: testMattermostChannels(),
	}})
	if activate == nil {
		t.Fatal("server activation did not select the initial channel")
	}
	_, _ = a.Update(activate())

	a.SetMode(ModeInsert)
	a.focusedPanel = PanelMessages
	_ = a.compose.Focus()
	typeInMattermostCompose(t, a, "hello @Ali")
	if !a.compose.IsMentionActive() {
		t.Fatal("ordinary user mention picker did not open")
	}
	if cmd := a.handleInsertMode(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("selecting an ordinary user should only update compose")
	}
	if got := a.compose.Value(); got != "hello @Alice " {
		t.Fatalf("selected mention inserted %q, want display mention", got)
	}

	submit := a.handleInsertMode(tea.KeyPressMsg{Code: tea.KeyEnter})
	if submit == nil {
		t.Fatal("submitting selected ordinary user mention returned nil command")
	}
	submitted := submit()
	msg, ok := submitted.(SendMessageMsg)
	if !ok {
		t.Fatalf("compose submission returned %T, want SendMessageMsg", submitted)
	}
	_, send := a.Update(msg)
	if send == nil {
		t.Fatal("Mattermost reducer returned nil send command")
	}
	_ = send()

	if len(service.requests) != 1 {
		t.Fatalf("Mattermost requests = %#v, want one", service.requests)
	}
	if got, want := service.requests[0].Text, "hello <@user-2> "; got != want {
		t.Fatalf("MattermostSendRequest.Text = %q, want %q", got, want)
	}
}

func mattermostRowByCorrelation(t *testing.T, model *messages.Model, correlationID string) messages.MessageItem {
	t.Helper()
	row, ok := model.FindMessageByCorrelationID(correlationID)
	if !ok {
		t.Fatalf("model missing correlation %q: %#v", correlationID, model.Messages())
	}
	return row
}

func TestMattermostComposeMentionSelectionSubmitsUserID(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	a.SetUserNames(map[string]string{"mattermost-user-id": "Alice"})
	a.focusedPanel = PanelMessages
	a.SetMode(ModeInsert)
	a.compose.Focus()

	for _, r := range "@Ali" {
		_ = handleInsertMode(a, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !a.compose.IsMentionActive() {
		t.Fatal("mention picker did not open")
	}
	_ = handleInsertMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := a.compose.Value(); got != "@Alice " {
		t.Fatalf("selected mention=%q want %q", got, "@Alice ")
	}
	_ = handleInsertMode(a, tea.KeyPressMsg{Code: tea.KeyBackspace})

	sendCmd := handleInsertMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if sendCmd == nil {
		t.Fatal("mention submission returned nil command")
	}
	msg := sendCmd()
	sendMsg, ok := msg.(SendMessageMsg)
	if !ok {
		t.Fatalf("submission command returned %T, want SendMessageMsg", msg)
	}
	_, serviceCmd := a.Update(sendMsg)
	if serviceCmd == nil {
		t.Fatal("Mattermost send reducer returned nil service command")
	}
	_ = serviceCmd()

	if len(service.requests) != 1 {
		t.Fatalf("requests=%#v want one Mattermost send", service.requests)
	}
	if got := service.requests[0].Text; got != "<@mattermost-user-id>" {
		t.Fatalf("request text=%q want %q", got, "<@mattermost-user-id>")
	}
	if strings.Contains(service.requests[0].Text, "subteam") {
		t.Fatalf("request text contains Slack usergroup token: %q", service.requests[0].Text)
	}
}

func TestMattermostSendFansOutOptimisticSuccessAndFailureToSameChannelWindows(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			service := &recordingMattermostSendService{}
			a := newMattermostSendApp(t, service)
			w1 := a.focusedWin
			if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
				t.Fatal("split failed")
			}
			w2 := a.focusedWin
			request := a.activeHistoryRequest
			_, sendCmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "fanout"})
			row1 := a.winModels[w1].Messages()[0]
			row2 := a.winModels[w2].Messages()[0]
			if row1.CorrelationID == "" || row1.CorrelationID != row2.CorrelationID || row1.DeliveryState != messages.DeliveryPending || row2.DeliveryState != messages.DeliveryPending {
				t.Fatalf("optimistic rows w1=%#v w2=%#v", row1, row2)
			}
			correlationID := row1.CorrelationID
			if failure {
				service.result = MattermostMessageSendFailedMsg{Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "fanout", CorrelationID: correlationID}, Reason: "safe"}
			} else {
				service.result = MattermostMessageSentMsg{Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "fanout", CorrelationID: correlationID}, Message: messages.MessageItem{ID: "opaque/post:id", CorrelationID: correlationID, Text: "authoritative"}}
			}
			_, _ = a.Update(sendCmd())
			for _, win := range []wintree.LeafID{w1, w2} {
				row := mattermostRowByCorrelation(t, a.winModels[win], correlationID)
				if failure && row.DeliveryState != messages.DeliveryFailed {
					t.Fatalf("window %v row=%#v want failed", win, row)
				}
				if !failure && (row.ID != "opaque/post:id" || row.Text != "authoritative") {
					t.Fatalf("window %v row=%#v want authoritative", win, row)
				}
			}
		})
	}
}

func TestMattermostRetryMarksEveryCopyPendingAndKeepsOriginalRequest(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	w2 := a.focusedWin
	request := a.activeHistoryRequest
	_, sendCmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "retry fanout"})
	correlationID := a.winModels[w1].Messages()[0].CorrelationID
	service.result = MattermostMessageSendFailedMsg{Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "retry fanout", CorrelationID: correlationID}}
	_, _ = a.Update(sendCmd())

	retryCmd := handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if retryCmd == nil {
		t.Fatal("retry command is nil")
	}
	for _, win := range []wintree.LeafID{w1, w2} {
		if row := mattermostRowByCorrelation(t, a.winModels[win], correlationID); row.DeliveryState != messages.DeliveryPending {
			t.Fatalf("window %v row=%#v want pending", win, row)
		}
	}
	_ = retryCmd()
	if len(service.requests) != 2 || !reflect.DeepEqual(service.requests[0], service.requests[1]) {
		t.Fatalf("requests=%#v want identical retry", service.requests)
	}
}

func TestMattermostSendCompletesAcrossFocusSwitchAndSplitAfterInsertion(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			service := &recordingMattermostSendService{}
			a := newMattermostSendApp(t, service)
			w1 := a.focusedWin
			request := a.activeHistoryRequest
			_, sendCmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "in flight"})
			correlationID := a.winModels[w1].Messages()[0].CorrelationID
			if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
				t.Fatal("split after insertion failed")
			}
			w2 := a.focusedWin
			_ = a.focusWindow(w1)
			resultRequest := MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "in flight", CorrelationID: correlationID}
			if failure {
				service.result = MattermostMessageSendFailedMsg{Request: resultRequest}
			} else {
				service.result = MattermostMessageSentMsg{Request: resultRequest, Message: messages.MessageItem{ID: "post/after:split", CorrelationID: correlationID, Text: "done"}}
			}
			_, _ = a.Update(sendCmd())
			for _, win := range []wintree.LeafID{w1, w2} {
				row := mattermostRowByCorrelation(t, a.winModels[win], correlationID)
				if failure && row.DeliveryState != messages.DeliveryFailed {
					t.Fatalf("window %v row=%#v want failed", win, row)
				}
				if !failure && (row.ID != "post/after:split" || row.DeliveryState != messages.DeliverySent) {
					t.Fatalf("window %v row=%#v want completed", win, row)
				}
			}
		})
	}
}

func TestMattermostSendOptimisticallyInsertsPlainRowAndDispatchesExactScope(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	a.SetNowTimestampFormatter(func() string { return "9:41 AM" })
	request := a.activeHistoryRequest
	historyCtx := a.activeHistoryContext

	_, cmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "hello mattermost"})
	if cmd == nil {
		t.Fatal("Mattermost send returned nil command")
	}
	rows := a.messagepane.Messages()
	if len(rows) != 1 {
		t.Fatalf("rows=%#v want one optimistic row", rows)
	}
	row := rows[0]
	if row.ID == "" || row.TS != "" || row.ID != row.CorrelationID {
		t.Fatalf("optimistic identity ID=%q TS=%q correlation=%q", row.ID, row.TS, row.CorrelationID)
	}
	if row.DeliveryServerID != string(request.ServerID) || row.DeliveryChannelID != request.ChannelID || row.DeliveryGeneration != request.Generation {
		t.Fatalf("optimistic scope=%q/%q/%d want %q/%q/%d", row.DeliveryServerID, row.DeliveryChannelID, row.DeliveryGeneration, request.ServerID, request.ChannelID, request.Generation)
	}
	if row.DeliveryState != messages.DeliveryPending {
		t.Fatalf("optimistic delivery row=%#v", row)
	}
	if row.UserID != "user-1" || row.UserName != "you" || row.Text != "hello mattermost" || row.Timestamp != "9:41 AM" || row.CreatedAt == 0 {
		t.Fatalf("optimistic content=%#v", row)
	}

	_ = cmd()
	want := MattermostSendRequest{
		ServerID:      request.ServerID,
		ChannelID:     request.ChannelID,
		Generation:    request.Generation,
		Text:          "hello mattermost",
		CorrelationID: row.CorrelationID,
	}
	if !reflect.DeepEqual(service.requests, []MattermostSendRequest{want}) {
		t.Fatalf("requests=%#v want %#v", service.requests, []MattermostSendRequest{want})
	}
	if len(service.contexts) != 1 || service.contexts[0] != historyCtx {
		t.Fatalf("send did not receive captured active history context")
	}
}

func TestMattermostSendCreatesUniqueInMemoryLocalIDs(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	_, _ = a.Update(SendMessageMsg{ChannelID: a.activeChannelID, Text: "one"})
	_, _ = a.Update(SendMessageMsg{ChannelID: a.activeChannelID, Text: "two"})
	rows := a.messagepane.Messages()
	if len(rows) != 2 || rows[0].ID == rows[1].ID || rows[0].CorrelationID == rows[1].CorrelationID {
		t.Fatalf("local identities are not unique: %#v", rows)
	}
}

func TestMattermostFirstSendCorrelationDiffersAcrossFreshAppsAndUsesSafeBoundedFormat(t *testing.T) {
	first := newMattermostSendApp(t, &recordingMattermostSendService{})
	second := newMattermostSendApp(t, &recordingMattermostSendService{})
	_, _ = first.Update(SendMessageMsg{ChannelID: first.activeChannelID, Text: "one"})
	_, _ = second.Update(SendMessageMsg{ChannelID: second.activeChannelID, Text: "two"})
	firstID := first.messagepane.Messages()[0].CorrelationID
	secondID := second.messagepane.Messages()[0].CorrelationID
	if firstID == secondID {
		t.Fatalf("fresh Apps reused first correlation ID %q", firstID)
	}
	valid := regexp.MustCompile(`^mmk-[a-zA-Z0-9_-]+$`)
	for _, id := range []string{firstID, secondID} {
		if len(id) == 0 || len(id) > 256 || !valid.MatchString(id) {
			t.Fatalf("correlation ID %q is not safe bounded ASCII", id)
		}
		if strings.Contains(id, "user-1") || strings.Contains(id, "server-1") {
			t.Fatalf("correlation ID %q contains application identity", id)
		}
	}
}

func TestMattermostCorrelationGeneratorFailureCreatesNoPendingRowOrPOST(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	a.mattermostCorrelationID = func() (string, error) {
		return "", errors.New("entropy-secret")
	}

	_, cmd := a.Update(SendMessageMsg{ChannelID: a.activeChannelID, Text: "must not send"})
	if cmd == nil {
		t.Fatal("generator failure did not return a safe failure command")
	}
	if rows := a.messagepane.Messages(); len(rows) != 0 {
		t.Fatalf("generator failure inserted rows=%#v", rows)
	}
	msg := cmd()
	failed, ok := msg.(statusbar.SendFailedMsg)
	if !ok || failed.Reason != "message send failed" || strings.Contains(failed.Reason, "entropy-secret") {
		t.Fatalf("failure message=%#v", msg)
	}
	if len(service.requests) != 0 {
		t.Fatalf("generator failure dispatched POST requests=%#v", service.requests)
	}
}

func TestMattermostSendExactSuccessReplacesPendingRow(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	request := a.activeHistoryRequest
	_, cmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "hello"})
	correlationID := a.messagepane.Messages()[0].CorrelationID
	service.result = MattermostMessageSentMsg{
		Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "hello", CorrelationID: correlationID},
		Message: messages.MessageItem{ID: "post-1", Text: "authoritative"},
	}

	_, _ = a.Update(cmd())
	rows := a.messagepane.Messages()
	if len(rows) != 1 || rows[0].ID != "post-1" || rows[0].Text != "authoritative" || rows[0].DeliveryState != messages.DeliverySent {
		t.Fatalf("rows=%#v want authoritative replacement", rows)
	}
}

func TestMattermostSendExactFailureLeavesVisibleFailedRowWithoutRenderingReason(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	request := a.activeHistoryRequest
	_, cmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "retry me"})
	correlationID := a.messagepane.Messages()[0].CorrelationID
	const secret = "PAT=mm-secret-value"
	service.result = MattermostMessageSendFailedMsg{
		Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "retry me", CorrelationID: correlationID},
		Reason:  "rejected\n" + secret,
	}

	_, toast := a.Update(cmd())
	rows := a.messagepane.Messages()
	if len(rows) != 1 || rows[0].DeliveryState != messages.DeliveryFailed || rows[0].Text != "retry me" {
		t.Fatalf("rows=%#v want visible failed row", rows)
	}
	if rows[0].FailureReason != "message send failed" || strings.Contains(rows[0].FailureReason, secret) {
		t.Fatalf("stored failure reason=%q exposed remote details", rows[0].FailureReason)
	}
	if strings.Contains(a.messagepane.View(20, 80), secret) {
		t.Fatal("rendered failed row exposed failure details")
	}
	if toast == nil {
		t.Fatal("failure did not return a generic toast command")
	}
}

func TestMattermostRetryUsesSameRowCorrelationTextAndScope(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	request := a.activeHistoryRequest
	_, firstCmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "same text"})
	correlationID := a.messagepane.Messages()[0].CorrelationID
	service.result = MattermostMessageSendFailedMsg{
		Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: request.ChannelID, Generation: request.Generation, Text: "same text", CorrelationID: correlationID},
		Reason:  "temporary",
	}
	_, _ = a.Update(firstCmd())

	retryCmd := handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if retryCmd == nil {
		t.Fatal("Enter on failed Mattermost row did not retry")
	}
	rows := a.messagepane.Messages()
	if len(rows) != 1 || rows[0].CorrelationID != correlationID || rows[0].DeliveryState != messages.DeliveryPending {
		t.Fatalf("retry mutated optimistic identity: %#v", rows)
	}
	_ = retryCmd()
	if len(service.requests) != 2 || !reflect.DeepEqual(service.requests[0], service.requests[1]) {
		t.Fatalf("retry requests=%#v want identical scope/text/correlation", service.requests)
	}
}

func TestMattermostSendIgnoresStaleServerChannelAndGenerationResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*MattermostSendRequest)
	}{
		{name: "server", mutate: func(r *MattermostSendRequest) { r.ServerID = "other-server" }},
		{name: "channel", mutate: func(r *MattermostSendRequest) { r.ChannelID = "other-channel" }},
		{name: "generation", mutate: func(r *MattermostSendRequest) { r.Generation++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, failure := range []bool{false, true} {
				t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
					a := newMattermostSendApp(t, &recordingMattermostSendService{})
					active := a.activeHistoryRequest
					_, _ = a.Update(SendMessageMsg{ChannelID: active.ChannelID, Text: "unchanged"})
					before := a.messagepane.Messages()[0]
					request := MattermostSendRequest{ServerID: active.ServerID, ChannelID: active.ChannelID, Generation: active.Generation, Text: before.Text, CorrelationID: before.CorrelationID}
					tc.mutate(&request)
					if failure {
						_, _ = a.Update(MattermostMessageSendFailedMsg{Request: request, Reason: "stale"})
					} else {
						_, _ = a.Update(MattermostMessageSentMsg{Request: request, Message: messages.MessageItem{ID: "stale-authoritative"}})
					}
					if got := a.messagepane.Messages(); len(got) != 1 || !reflect.DeepEqual(got[0], before) {
						t.Fatalf("stale result changed rows from %#v to %#v", before, got)
					}
				})
			}
		})
	}
}

func TestMattermostDelayedFailureAfterAuthoritativeHistoryDoesNotDowngradeSplitRowsOrToast(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	w1 := a.focusedWin
	if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
		t.Fatal("split failed")
	}
	w2 := a.focusedWin
	request := a.activeHistoryRequest
	_, sendCmd := a.Update(SendMessageMsg{ChannelID: request.ChannelID, Text: "history wins"})
	correlationID := a.winModels[w1].Messages()[0].CorrelationID
	authoritative := messages.MessageItem{
		ID:            "opaque/post:id",
		CorrelationID: correlationID,
		DeliveryState: messages.DeliverySent,
		Text:          "authoritative history",
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{
		Request:          request,
		AuthoritativeIDs: []string{authoritative.ID},
		Messages:         []messages.MessageItem{authoritative},
		HasMore:          false,
	})
	service.result = MattermostMessageSendFailedMsg{
		Request: MattermostSendRequest{
			ServerID:      request.ServerID,
			ChannelID:     request.ChannelID,
			Generation:    request.Generation,
			Text:          "history wins",
			CorrelationID: correlationID,
		},
		Reason: "delayed failure",
	}

	_, toast := a.Update(sendCmd())
	if toast != nil {
		t.Fatal("delayed failure after authoritative history emitted a toast")
	}
	for _, win := range []wintree.LeafID{w1, w2} {
		rows := a.winModels[win].Messages()
		if len(rows) != 1 || !reflect.DeepEqual(rows[0], authoritative) {
			t.Fatalf("window %v rows=%#v want unchanged authoritative row", win, rows)
		}
		if rows[0].DeliveryState == messages.DeliveryFailed || rows[0].FailureReason != "" {
			t.Fatalf("window %v gained retry affordance: %#v", win, rows[0])
		}
	}
}

func TestMattermostSendContextIsCanceledByChannelNavigation(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	_, sendCmd := a.Update(SendMessageMsg{ChannelID: a.activeChannelID, Text: "cancel me"})
	captured := a.activeHistoryContext

	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	select {
	case <-captured.Done():
	default:
		t.Fatal("channel navigation did not cancel captured send context")
	}
	_ = sendCmd()
	if len(service.contexts) != 1 || service.contexts[0] != captured || service.contexts[0].Err() != context.Canceled {
		t.Fatalf("service contexts=%#v want canceled captured history context", service.contexts)
	}
}

func TestMattermostSendContextIsCanceledByServerNavigation(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, service)
	_, sendCmd := a.Update(SendMessageMsg{ChannelID: a.activeChannelID, Text: "cancel me"})
	captured := a.activeHistoryContext

	_, _ = a.Update(ServerSwitchedMsg{Server: ServerViewState{ServerID: "server-2"}})
	select {
	case <-captured.Done():
	default:
		t.Fatal("server navigation did not cancel captured send context")
	}
	_ = sendCmd()
	if len(service.contexts) != 1 || service.contexts[0] != captured || service.contexts[0].Err() != context.Canceled {
		t.Fatalf("service contexts=%#v want canceled captured history context", service.contexts)
	}
}

func TestMattermostTask10EnablesOnlySend(t *testing.T) {
	mm := MattermostTask10Features()
	for feature := FeatureThreads; feature <= FeatureSend; feature++ {
		if got, want := mm.Allows(feature), feature == FeatureSend; got != want {
			t.Fatalf("Mattermost feature %v allows=%v want %v", feature, got, want)
		}
	}
}
