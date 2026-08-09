package config

import (
	"strings"
	"testing"
)

func TestEditMattermostServerPreservesDocumentAndUpdatesInPlace(t *testing.T) {
	input := "# user comment\r\n[unknown]\r\nvalue = 42 # keep\r\n\r\n" +
		"[[mattermost_servers]]\r\n" +
		"id = 'first'\r\nurl = 'https://first.example'\r\ncustom = 'keep me'\r\n\r\n" +
		"# server comment\r\n[[mattermost_servers]]\r\n" +
		"id = 'target'\r\nurl = 'https://old.example' # replace\r\ndisplay_name = 'Old'\r\nuser_id = 'old-user'\r\nusername = 'old-name'\r\nextra = 7\r\n\r\n" +
		"[after]\r\nnested = { untouched = true }\r\n"

	server := MattermostServer{ID: "target", URL: "https://new.example", DisplayName: "New", UserID: "new-user", Username: "new-name"}
	got, err := EditMattermostServer([]byte(input), server)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{
		"# user comment\r\n[unknown]\r\nvalue = 42 # keep\r\n",
		"custom = 'keep me'\r\n",
		"# server comment\r\n[[mattermost_servers]]\r\n",
		"url = 'https://new.example' # replace\r\n",
		"display_name = 'New'\r\n",
		"user_id = 'new-user'\r\n",
		"username = 'new-name'\r\n",
		"extra = 7\r\n",
		"[after]\r\nnested = { untouched = true }\r\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("edited TOML missing %q:\n%s", want, text)
		}
	}
	if strings.Index(text, "id = 'first'") > strings.Index(text, "id = 'target'") {
		t.Fatal("server order changed")
	}
	if strings.Contains(text, "https://old.example") || strings.Contains(text, "Old'") {
		t.Fatalf("old fields remain:\n%s", text)
	}
	if strings.Contains(strings.ReplaceAll(text, "\r\n", ""), "\n") {
		t.Fatal("editor introduced non-CRLF line endings")
	}
}

func TestEditMattermostServerAppendsDeterministically(t *testing.T) {
	input := []byte("# keep\n[feature]\nenabled = true\n")
	server := MattermostServer{ID: "new", URL: "https://chat.example", DisplayName: "Chat", UserID: "user", Username: "alice"}
	got, err := EditMattermostServer(input, server)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := "\n[[mattermost_servers]]\nid = 'new'\nurl = 'https://chat.example'\ndisplay_name = 'Chat'\nuser_id = 'user'\nusername = 'alice'\n"
	if !strings.HasPrefix(string(got), string(input)) || !strings.HasSuffix(string(got), wantSuffix) {
		t.Fatalf("append =\n%s", got)
	}
}

func TestEditMattermostServerRejectsDuplicateIDAndMalformedTOML(t *testing.T) {
	duplicate := []byte("[[mattermost_servers]]\nid='dup'\n[[mattermost_servers]]\nid='dup'\n")
	if _, err := EditMattermostServer(duplicate, MattermostServer{ID: "dup"}); err == nil {
		t.Fatal("accepted duplicate server ID")
	}
	if _, err := EditMattermostServer([]byte("[broken\nvalue = 1\n"), MattermostServer{ID: "new"}); err == nil {
		t.Fatal("accepted malformed TOML")
	}
}

func TestEditMattermostServerUpdatesQuotedTableAndFieldKeys(t *testing.T) {
	input := []byte("[['mattermost_servers']]\n'id' = 'target'\n\"url\" = 'https://old.example'\ncustom = 'keep'\n")
	server := MattermostServer{ID: "target", URL: "https://new.example", DisplayName: "New", UserID: "user", Username: "alice"}

	got, err := EditMattermostServer(input, server)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Count(text, "mattermost_servers") != 1 || strings.Contains(text, "https://old.example") {
		t.Fatalf("quoted table was not updated in place:\n%s", text)
	}
	for _, want := range []string{"url = 'https://new.example'", "custom = 'keep'", "username = 'alice'"} {
		if !strings.Contains(text, want) {
			t.Errorf("edited TOML missing %q:\n%s", want, text)
		}
	}
}

func TestEditMattermostServerRejectsDuplicateIDsInUnsupportedRepresentation(t *testing.T) {
	input := []byte("mattermost_servers = [{ id = 'dup' }, { id = 'dup' }]\n")
	if _, err := EditMattermostServer(input, MattermostServer{ID: "dup"}); err == nil || !strings.Contains(err.Error(), "duplicate Mattermost server ID") {
		t.Fatalf("error = %v", err)
	}
}
