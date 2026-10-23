// Package tokenfile reads and writes studiolink.env, the file that hands the
// bridge's token to agents without anyone copying it by hand.
//
// The file is in the KEY=value form that Docker's --env-file and Compose's
// env_file read, so a container can load it as STUDIOLINK_TOKEN, and
// studiolink-client reads it directly. A file holding only the token (a
// Docker secret, say) is accepted too.
package tokenfile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Name is the file's name next to studiolink.json.
const Name = "studiolink.env"

// Key is the variable the file sets, and the one agents read.
const Key = "STUDIOLINK_TOKEN"

// PathFor returns where the token file for a config lives.
func PathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), Name)
}

// Write saves token to path, readable by the current user only where the OS
// honours modes. It leaves an identical file alone.
func Write(path, token string) error {
	data := []byte("# Written by studiolink-host from studiolink.json; edits are overwritten.\n" +
		"# Load it with docker run --env-file, or env_file in Compose.\n" +
		Key + "=" + token + "\n")
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Parse returns the token in a studiolink.env file or a bare token file.
func Parse(data []byte) (string, error) {
	text := clean(data)
	if !strings.Contains(text, "=") {
		if token := strings.TrimSpace(text); token != "" {
			return token, nil
		}
		return "", errors.New("the file is empty")
	}
	if token := Values(data)[Key]; token != "" {
		return token, nil
	}
	return "", fmt.Errorf("no %s= line", Key)
}

// Values returns the KEY=value lines of an env file, skipping comments.
func Values(data []byte) map[string]string {
	vals := map[string]string{}
	for line := range strings.SplitSeq(clean(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		vals[key] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return vals
}

func clean(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// WriteValues saves an env file of the given variables, in order.
func WriteValues(path, comment string, kv ...string) error {
	var b strings.Builder
	b.WriteString(comment)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(kv[i] + "=" + kv[i+1] + "\n")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

// Read returns the token in the file at path.
func Read(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token, err := Parse(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return token, nil
}
