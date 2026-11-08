package logging

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	credentialName = regexp.MustCompile(`(?i)(^|[_\-.])(key|apikey|api_key|token|secret|password|passwd|pass|pwd|credential|credentials|cookie|authorization|auth|jwt)([_\-.]|$)`)
	urlPattern     = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>]+`)
	bearerPattern  = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	assignment     = regexp.MustCompile(`(?i)(["']?([a-z0-9_.-]*(?:key|token|secret|password|passwd|pass|pwd|credential|cookie|authorization)[a-z0-9_.-]*)["']?\s*[:=]\s*)("(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|[^\s,;]+)`)
)

func sensitiveName(name string) bool {
	upper := strings.ToUpper(name)
	if upper == "PWD" || upper == "OLDPWD" || upper == "SSH_AUTH_SOCK" || upper == "HOTMOUSE_AUTH_REQUIRED" || upper == "AUTH_POLICY" || strings.HasSuffix(upper, "_TOKENS") || strings.HasSuffix(upper, "_TOKEN_COUNT") {
		return false
	}
	compact := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(name))
	for _, fragment := range []string{"apikey", "accesstoken", "authtoken", "clientsecret", "privatekey", "password", "credential", "authorization"} {
		if strings.Contains(compact, fragment) {
			return true
		}
	}
	return credentialName.MatchString(name)
}

type redactor struct{ secrets []string }

func newRedactor(environment []string) *redactor {
	return (&redactor{}).withEnvironment(environment)
}

func (r *redactor) withEnvironment(environment []string) *redactor {
	values := map[string]bool{}
	for _, secret := range r.secrets {
		values[secret] = true
	}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || !sensitiveName(key) {
			continue
		}
		values[value] = true
		if strings.ContainsAny(value, "\r\n") {
			for _, line := range strings.FieldsFunc(value, func(char rune) bool { return char == '\n' || char == '\r' }) {
				if len(line) >= 4 {
					values[line] = true
				}
			}
		}
		values[url.QueryEscape(value)] = true
		quoted := strconv.Quote(value)
		values[quoted[1:len(quoted)-1]] = true
		if data, err := json.Marshal(value); err == nil {
			values[string(data[1:len(data)-1])] = true
		}
	}
	copy := &redactor{}
	for value := range values {
		copy.secrets = append(copy.secrets, value)
	}
	sort.Slice(copy.secrets, func(i, j int) bool { return len(copy.secrets[i]) > len(copy.secrets[j]) })
	return copy
}

func (r *redactor) text(value string) string {
	value = urlPattern.ReplaceAllStringFunc(value, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[invalid URL redacted]"
		}
		if u.User != nil {
			u.User = url.User("redacted")
		}
		query := u.Query()
		for key := range query {
			if sensitiveName(key) {
				query.Set(key, "[redacted]")
			}
		}
		u.RawQuery = query.Encode()
		return u.String()
	})
	for _, secret := range r.secrets {
		if len(secret) < 4 {
			pattern := regexp.MustCompile(`(^|[^a-zA-Z0-9_])` + regexp.QuoteMeta(secret) + `($|[^a-zA-Z0-9_])`)
			value = pattern.ReplaceAllString(value, "${1}[redacted]${2}")
		} else {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	value = bearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	return assignment.ReplaceAllStringFunc(value, func(match string) string {
		parts := assignment.FindStringSubmatch(match)
		if !sensitiveName(parts[2]) {
			return match
		}
		masked := "[redacted]"
		if strings.HasPrefix(parts[3], `"`) {
			masked = `"[redacted]"`
		} else if strings.HasPrefix(parts[3], "'") {
			masked = "'[redacted]'"
		}
		return parts[1] + masked
	})
}

func (r *redactor) jsonValue(value any) any {
	switch v := value.(type) {
	case string:
		return r.text(v)
	case []any:
		for i := range v {
			v[i] = r.jsonValue(v[i])
		}
	case map[string]any:
		for key, child := range v {
			if sensitiveName(key) {
				if text, ok := child.(string); ok && text != "" {
					v[key] = "[redacted]"
					continue
				}
			}
			v[key] = r.jsonValue(child)
		}
	}
	return value
}
