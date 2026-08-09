package mattermost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinSecretStoreUsesNativeKeychainAPI(t *testing.T) {
	path := filepath.Join("secret_store_darwin.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, forbidden := range []string{"os/exec", "/usr/bin/security", "exec.Command", `"-w"`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Darwin adapter contains forbidden subprocess behavior %q", forbidden)
		}
	}
	for _, required := range []string{"Security.framework", "SecKeychainFindGenericPassword", "SecKeychainAddGenericPassword", "SecKeychainItemModifyAttributesAndData", "SecKeychainItemDelete", "SecKeychainItemFreeContent"} {
		if !strings.Contains(text, required) {
			t.Errorf("Darwin adapter missing native API %q", required)
		}
	}
}
