package clientconfig

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const DefaultServer = "http://127.0.0.1:8080"

var Defaults = map[string]string{"server": DefaultServer, "creator": "cli", "timeout": "30s", "output": "table", "color": "auto"}
var Keys = []string{"server", "project", "creator", "worker", "timeout", "output", "color", "token_file"}

type Config struct {
	SchemaVersion  int                          `yaml:"schema_version" json:"schema_version"`
	CurrentProfile string                       `yaml:"current_profile" json:"current_profile"`
	Profiles       map[string]map[string]string `yaml:"profiles" json:"profiles"`
}

type Store struct {
	Path   string
	Config Config
}

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hotmouse", "config.yaml"), nil
}

func NormalizeServer(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("server must be an http(s) URL without credentials, query or fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if u.Port() == "80" && u.Scheme == "http" || u.Port() == "443" && u.Scheme == "https" {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func Validate(key, value string) error {
	known := false
	for _, k := range Keys {
		known = known || key == k
	}
	if !known {
		return fmt.Errorf("unknown configuration key %q", key)
	}
	switch key {
	case "server":
		_, err := NormalizeServer(value)
		return err
	case "timeout":
		d, e := time.ParseDuration(value)
		if e != nil || d <= 0 {
			return fmt.Errorf("timeout must be a positive duration")
		}
	case "output":
		if value != "table" && value != "json" && value != "yaml" {
			return fmt.Errorf("output must be table, json or yaml")
		}
	case "color":
		if value != "auto" && value != "always" && value != "never" {
			return fmt.Errorf("color must be auto, always or never")
		}
	case "creator", "worker", "project":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be empty; use unset to remove it", key)
		}
	}
	return nil
}

func Load(path string) (*Store, error) {
	if path == "" {
		var e error
		path, e = DefaultPath()
		if e != nil {
			return nil, e
		}
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	s := &Store{Path: path, Config: Config{SchemaVersion: 1, CurrentProfile: "local", Profiles: map[string]map[string]string{"local": {}}}}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err = dec.Decode(&s.Config); err != nil {
		return nil, fmt.Errorf("invalid configuration file: %w", err)
	}
	var extra any
	if err = dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("configuration must contain one document")
	}
	if s.Config.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported configuration schema_version")
	}
	if s.Config.Profiles == nil || s.Config.Profiles[s.Config.CurrentProfile] == nil {
		return nil, fmt.Errorf("current profile does not exist")
	}
	for name, p := range s.Config.Profiles {
		if !ValidName(name) {
			return nil, fmt.Errorf("invalid profile name")
		}
		for k, v := range p {
			if err = Validate(k, v); err != nil {
				return nil, err
			}
		}
	}
	return s, nil
}

func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func (s *Store) Save() error {
	b, e := yaml.Marshal(s.Config)
	if e != nil {
		return e
	}
	return WritePrivate(s.Path, b, true)
}

// WritePrivate uses a same-directory temporary file, so readers never see a
// partially written configuration, credential store or export.
func WritePrivate(path string, data []byte, overwrite bool) error {
	dir := filepath.Dir(path)
	_, statErr := os.Stat(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if os.IsNotExist(statErr) {
		if err := privatePermissions(dir, true); err != nil {
			return err
		}
	}
	if !overwrite {
		if _, e := os.Lstat(path); e == nil {
			return fmt.Errorf("%w; use --overwrite", os.ErrExist)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	f, e := os.CreateTemp(dir, ".hotmouse-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if e = privatePermissions(tmp, false); e == nil {
		_, e = f.Write(data)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if overwrite {
		return os.Rename(tmp, path)
	}
	// Linking publishes a new file without a check/rename overwrite race.
	if e = os.Link(tmp, path); e != nil {
		return e
	}
	return nil
}

func (s *Store) credentialsPath() string {
	return filepath.Join(filepath.Dir(s.Path), "credentials.json")
}

type credentials map[string]*http.Cookie

func (s *Store) readCredentials() (credentials, error) {
	b, e := os.ReadFile(s.credentialsPath())
	if os.IsNotExist(e) {
		return credentials{}, nil
	}
	if e != nil {
		return nil, e
	}
	var c credentials
	if json.Unmarshal(b, &c) != nil {
		return nil, fmt.Errorf("invalid credentials file")
	}
	if c == nil {
		c = credentials{}
	}
	return c, nil
}
func credentialKey(profile, server string) string { return profile + "\n" + server }
func (s *Store) Cookie(profile, server string) (*http.Cookie, error) {
	c, e := s.readCredentials()
	if e != nil {
		return nil, e
	}
	cookie := c[credentialKey(profile, server)]
	if cookie != nil && (!cookie.Expires.IsZero() && time.Now().After(cookie.Expires) || cookie.MaxAge < 0) {
		return nil, nil
	}
	return cookie, nil
}
func (s *Store) SaveCookie(profile, server string, cookie *http.Cookie) error {
	c, e := s.readCredentials()
	if e != nil {
		return e
	}
	key := credentialKey(profile, server)
	if cookie == nil {
		delete(c, key)
	} else {
		c[key] = cookie
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return WritePrivate(s.credentialsPath(), append(b, '\n'), true)
}
func (s *Store) RemoveCredentials(profile string) error {
	c, e := s.readCredentials()
	if e != nil {
		return e
	}
	for k := range c {
		if strings.HasPrefix(k, profile+"\n") {
			delete(c, k)
		}
	}
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	return WritePrivate(s.credentialsPath(), append(b, '\n'), true)
}
