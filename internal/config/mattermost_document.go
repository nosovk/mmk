package config

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

var (
	tomlTableHeaderRE = regexp.MustCompile(`^\s*\[\[?.*\]\]?\s*(?:#.*)?$`)
	serverHeaderRE    = regexp.MustCompile(`^\s*\[\[\s*(?:mattermost_servers|'mattermost_servers'|"mattermost_servers")\s*\]\]\s*(?:#.*)?$`)
	serverFieldRE     = regexp.MustCompile(`^(\s*)((?:id|url|display_name|user_id|username)|'(?:id|url|display_name|user_id|username)'|"(?:id|url|display_name|user_id|username)")\s*=.*?(\s+#.*)?$`)
)

type documentLine struct {
	text string
	eol  string
}

type serverBlock struct {
	start int
	end   int
	id    string
}

// EditMattermostServer updates or appends one top-level Mattermost server
// table while preserving all unrelated TOML bytes and unowned server fields.
func EditMattermostServer(document []byte, server MattermostServer) ([]byte, error) {
	if strings.TrimSpace(server.ID) == "" {
		return nil, errors.New("Mattermost server ID must not be empty")
	}
	var parsed map[string]any
	if len(document) > 0 {
		if err := toml.Unmarshal(document, &parsed); err != nil {
			return nil, fmt.Errorf("parse config TOML: %w", err)
		}
	}
	if err := validateMattermostServerIDs(parsed); err != nil {
		return nil, err
	}

	lines := splitDocumentLines(document)
	blocks, err := mattermostServerBlocks(lines)
	if err != nil {
		return nil, err
	}
	target := -1
	seen := make(map[string]struct{}, len(blocks))
	for i, block := range blocks {
		if _, duplicate := seen[block.id]; duplicate {
			return nil, fmt.Errorf("duplicate Mattermost server ID %q", block.id)
		}
		seen[block.id] = struct{}{}
		if block.id == server.ID {
			target = i
		}
	}

	newline := documentNewline(lines)
	if target < 0 {
		return appendMattermostServer(document, server, newline), nil
	}
	block := blocks[target]
	fields := serverFieldLines(server)
	found := make(map[string]bool, len(fields))
	out := append([]documentLine(nil), lines[:block.start+1]...)
	for _, line := range lines[block.start+1 : block.end] {
		match := serverFieldRE.FindStringSubmatch(line.text)
		if match == nil {
			out = append(out, line)
			continue
		}
		key := strings.Trim(match[2], `'"`)
		if found[key] {
			return nil, fmt.Errorf("duplicate field %q in Mattermost server %q", key, server.ID)
		}
		found[key] = true
		out = append(out, documentLine{text: match[1] + fields[key] + match[3], eol: line.eol})
	}
	for _, key := range []string{"id", "url", "display_name", "user_id", "username"} {
		if !found[key] {
			out = append(out, documentLine{text: fields[key], eol: newline})
		}
	}
	out = append(out, lines[block.end:]...)
	return joinDocumentLines(out), nil
}

func validateMattermostServerIDs(parsed map[string]any) error {
	value, ok := parsed["mattermost_servers"]
	if !ok {
		return nil
	}
	servers, ok := value.([]any)
	if !ok {
		return errors.New("mattermost_servers must be an array of tables")
	}
	seen := make(map[string]struct{}, len(servers))
	for _, value := range servers {
		server, ok := value.(map[string]any)
		if !ok {
			return errors.New("mattermost_servers must contain only tables")
		}
		id, ok := server["id"].(string)
		if !ok || strings.TrimSpace(id) == "" {
			return errors.New("Mattermost server block is missing id")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate Mattermost server ID %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func mattermostServerBlocks(lines []documentLine) ([]serverBlock, error) {
	var blocks []serverBlock
	for i := 0; i < len(lines); i++ {
		if !serverHeaderRE.MatchString(lines[i].text) {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if tomlTableHeaderRE.MatchString(lines[j].text) {
				end = j
				break
			}
		}
		var decoded struct {
			Servers []MattermostServer `toml:"mattermost_servers"`
		}
		if err := toml.Unmarshal(joinDocumentLines(lines[i:end]), &decoded); err != nil {
			return nil, fmt.Errorf("parse Mattermost server block: %w", err)
		}
		if len(decoded.Servers) != 1 || decoded.Servers[0].ID == "" {
			return nil, errors.New("Mattermost server block is missing id")
		}
		blocks = append(blocks, serverBlock{start: i, end: end, id: decoded.Servers[0].ID})
		i = end - 1
	}
	return blocks, nil
}

func appendMattermostServer(document []byte, server MattermostServer, newline string) []byte {
	var out bytes.Buffer
	out.Write(document)
	if len(document) > 0 && !bytes.HasSuffix(document, []byte("\n")) && !bytes.HasSuffix(document, []byte("\r")) {
		out.WriteString(newline)
	}
	if len(document) > 0 {
		out.WriteString(newline)
	}
	out.WriteString("[[mattermost_servers]]" + newline)
	fields := serverFieldLines(server)
	for _, key := range []string{"id", "url", "display_name", "user_id", "username"} {
		out.WriteString(fields[key] + newline)
	}
	return out.Bytes()
}

func serverFieldLines(server MattermostServer) map[string]string {
	encoded, _ := toml.Marshal(struct {
		ID          string `toml:"id"`
		URL         string `toml:"url"`
		DisplayName string `toml:"display_name"`
		UserID      string `toml:"user_id"`
		Username    string `toml:"username"`
	}{server.ID, server.URL, server.DisplayName, server.UserID, server.Username})
	fields := make(map[string]string, 5)
	for _, line := range strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n") {
		if match := serverFieldRE.FindStringSubmatch(line); match != nil {
			fields[strings.Trim(match[2], `'"`)] = line
		}
	}
	return fields
}

func splitDocumentLines(document []byte) []documentLine {
	if len(document) == 0 {
		return nil
	}
	var lines []documentLine
	for len(document) > 0 {
		index := bytes.IndexByte(document, '\n')
		if index < 0 {
			lines = append(lines, documentLine{text: string(document)})
			break
		}
		text := document[:index]
		eol := "\n"
		if len(text) > 0 && text[len(text)-1] == '\r' {
			text = text[:len(text)-1]
			eol = "\r\n"
		}
		lines = append(lines, documentLine{text: string(text), eol: eol})
		document = document[index+1:]
	}
	return lines
}

func joinDocumentLines(lines []documentLine) []byte {
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(line.text)
		out.WriteString(line.eol)
	}
	return []byte(out.String())
}

func documentNewline(lines []documentLine) string {
	for _, line := range lines {
		if line.eol != "" {
			return line.eol
		}
	}
	return "\n"
}
