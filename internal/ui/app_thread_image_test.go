package ui

import (
	"bytes"
	stdimage "image"
	"image/color"
	imgpng "image/png"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	imgpkg "github.com/nosovk/mmk/internal/image"
	"github.com/nosovk/mmk/internal/ui/imgrender"
	"github.com/nosovk/mmk/internal/ui/messages"
)

func TestImageReadyMsg_RootWithRepliesRebuildsAndEmitsKittyUploadOnce(t *testing.T) {
	imageData := stdimage.NewRGBA(stdimage.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			imageData.Set(x, y, color.RGBA{R: 32, G: 96, B: 160, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := imgpng.Encode(&encoded, imageData); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(encoded.Bytes())
	}))
	t.Cleanup(server.Close)

	cache, err := imgpkg.NewCache(t.TempDir(), 10)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	fetcher := imgpkg.NewFetcher(cache, nil)
	ready := make(chan imgrender.ImageReadyMsg, 1)
	app := NewApp()
	app.SetImageContext(imgrender.ImageContext{
		Protocol:    imgpkg.ProtoKitty,
		Fetcher:     fetcher,
		KittyRender: imgpkg.NewKittyRenderer(imgpkg.NewRegistry()),
		CellPixels:  stdimage.Pt(8, 16),
		MaxRows:     12,
		SendMsg: func(msg tea.Msg) {
			if imageReady, ok := msg.(imgrender.ImageReadyMsg); ok {
				ready <- imageReady
			}
		},
	})
	root := messages.MessageItem{
		ID: "root-1", UserName: "alice", Text: "root image",
		Attachments: []messages.Attachment{{
			Kind: "image", Name: "root.png", URL: "https://mattermost.example/files/root.png",
			FileID: "root-image", Mime: "image/png",
			Thumbs: []messages.ThumbSpec{{URL: server.URL + "/root-preview.png", W: 320, H: 240}},
		}},
	}
	reply := messages.MessageItem{ID: "reply-1", RootID: "root-1", UserName: "bob", Text: "reply"}
	app.threadPanel.SetThread(root, []messages.MessageItem{reply}, "channel-1", "root-1")

	saved := imgpkg.KittyOutput
	t.Cleanup(func() { imgpkg.KittyOutput = saved })
	var uploaded bytes.Buffer
	imgpkg.KittyOutput = &uploaded
	_ = app.threadPanel.View(40, 60)
	if uploaded.Len() != 0 {
		t.Fatalf("placeholder view emitted %d unexpected Kitty bytes", uploaded.Len())
	}

	var imageReady imgrender.ImageReadyMsg
	select {
	case imageReady = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for root image fetch completion")
	}

	_, _ = app.Update(imageReady)
	_ = app.threadPanel.View(40, 60)
	if uploaded.Len() == 0 {
		t.Fatal("root ImageReadyMsg did not rebuild the thread view and emit Kitty upload bytes")
	}
	firstUploadBytes := uploaded.Len()
	_ = app.threadPanel.View(40, 60)
	if uploaded.Len() != firstUploadBytes {
		t.Fatalf("subsequent View emitted duplicate Kitty upload: first=%d total=%d", firstUploadBytes, uploaded.Len())
	}
}

// TestImageReadyMsg_RoutesToThread asserts that an ImageReadyMsg
// matching a thread reply's TS triggers the thread panel's cache
// invalidation. Without this routing, thread images stay as
// placeholders forever even after the bytes land in the cache.
func TestImageReadyMsg_RoutesToThread(t *testing.T) {
	app := NewApp()

	parent := messages.MessageItem{TS: "1.0", UserID: "U1", UserName: "alice"}
	reply := messages.MessageItem{
		TS: "1.001", UserID: "U2", UserName: "bob",
		Attachments: []messages.Attachment{{Kind: "image", FileID: "F999", Name: "x.png"}},
	}
	app.threadPanel.SetThread(parent, []messages.MessageItem{reply}, "C1", "1.0")

	// Force a View() so HasReply works (replyIDToIdx populates lazily).
	_ = app.threadPanel.View(20, 60)

	versionBefore := app.threadPanel.Version()

	var cmd tea.Cmd
	_, cmd = app.Update(imgrender.ImageReadyMsg{Channel: "C1", TS: "1.001", Key: "F999-720"})
	_ = cmd

	if app.threadPanel.Version() == versionBefore {
		t.Fatal("ImageReadyMsg for a thread reply did not invalidate the thread cache (Version did not bump)")
	}
}

// TestImageReadyMsg_DoesNotInvalidateThreadForUnknownTS asserts that
// the routing only fires when the open thread actually contains the
// referenced reply — otherwise we'd churn the thread cache on every
// messages-pane image arrival.
func TestImageReadyMsg_DoesNotInvalidateThreadForUnknownTS(t *testing.T) {
	app := NewApp()

	parent := messages.MessageItem{TS: "1.0", UserID: "U1", UserName: "alice"}
	reply := messages.MessageItem{TS: "1.001", UserID: "U2", UserName: "bob"}
	app.threadPanel.SetThread(parent, []messages.MessageItem{reply}, "C1", "1.0")
	_ = app.threadPanel.View(20, 60)

	versionBefore := app.threadPanel.Version()

	var cmd tea.Cmd
	_, cmd = app.Update(imgrender.ImageReadyMsg{Channel: "C9", TS: "999.999", Key: "F1-720"})
	_ = cmd

	if app.threadPanel.Version() != versionBefore {
		t.Fatal("ImageReadyMsg for a non-thread TS unexpectedly bumped thread Version")
	}
}
