package core

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// Match URL authorities in prose, including bracket-wrapped CTF URLs and
// bracketed IPv6 addresses. Paths and numeric hostname labels are never ports.
var targetHTTPAuthority = regexp.MustCompile(`(?i)https?://(?:[a-z0-9._~!$&()*+,;=:%-]+@)?(?:\[[0-9a-f:.%]+\]|[a-z0-9.-]+)(?::[0-9]+)?`)
var targetExplicitAddress = regexp.MustCompile(`(?i)(\[[0-9a-f:.]+\]|[a-z0-9][a-z0-9.-]*):([0-9]{1,5})\b`)

func targetHostKey(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

// TargetProbeURLCandidates parses URL authorities with net/url, rather than
// asking the model to infer ports from a hostname or path.
func TargetProbeURLCandidates(texts ...string) []TargetEndpoint {
	var targets []TargetEndpoint
	seen := map[string]bool{}
	for _, text := range texts {
		for _, match := range targetHTTPAuthority.FindAllStringIndex(text, -1) {
			// Do not turn a malformed explicit port into a default port.
			if match[1] < len(text) && text[match[1]] == ':' {
				continue
			}
			u, err := url.Parse(text[match[0]:match[1]])
			if err != nil || u.Hostname() == "" {
				continue
			}
			port := 80
			if strings.EqualFold(u.Scheme, "https") {
				port = 443
			}
			if u.Port() != "" {
				port, err = strconv.Atoi(u.Port())
				if err != nil || port < 1 || port > 65535 {
					continue
				}
			}
			target := TargetEndpoint{Host: u.Hostname(), Port: port}
			key := net.JoinHostPort(targetHostKey(target.Host), strconv.Itoa(port))
			if !seen[key] {
				seen[key] = true
				targets = append(targets, target)
			}
		}
	}
	return targets
}

func resolveTargetProbeURLPorts(analysis TargetProbeAnalysis, texts ...string) (TargetProbeAnalysis, error) {
	if !analysis.Required {
		return analysis, nil
	}
	ports := map[string]map[int]bool{}
	for _, target := range TargetProbeURLCandidates(texts...) {
		key := targetHostKey(target.Host)
		if ports[key] == nil {
			ports[key] = map[int]bool{}
		}
		ports[key][target.Port] = true
	}
	// The same host may also have a separately specified raw TCP service.
	// Preserve such explicit host:port pairs instead of forcing HTTP's port.
	for _, text := range texts {
		for _, match := range targetExplicitAddress.FindAllStringSubmatch(text, -1) {
			key := targetHostKey(strings.Trim(match[1], "[]"))
			if ports[key] == nil {
				continue
			}
			port, err := strconv.Atoi(match[2])
			if err == nil && port >= 1 && port <= 65535 {
				ports[key][port] = true
			}
		}
	}
	resolved := analysis
	resolved.Targets = make([]TargetEndpoint, 0, len(analysis.Targets))
	seen := map[string]bool{}
	for _, target := range analysis.Targets {
		allowed := ports[targetHostKey(target.Host)]
		if len(allowed) > 0 && !allowed[target.Port] {
			if len(allowed) != 1 {
				return analysis, fmt.Errorf("target probe port %d for %s is absent from the source URLs and explicit addresses; select a port actually specified in the project", target.Port, target.Host)
			}
			for port := range allowed {
				target.Port = port
			}
		}
		key := net.JoinHostPort(targetHostKey(target.Host), strconv.Itoa(target.Port))
		if !seen[key] {
			seen[key] = true
			resolved.Targets = append(resolved.Targets, target)
		}
	}
	return resolved, nil
}

func targetProbeSources(t *sql.Tx, project string) ([]string, error) {
	rows, err := t.Query(`SELECT description FROM facts WHERE project_id=? AND id=CASE WHEN EXISTS(SELECT 1 FROM facts WHERE project_id=? AND id='competition_target') THEN 'competition_target' ELSE 'origin' END UNION ALL SELECT description FROM goals WHERE project_id=? UNION ALL SELECT content FROM hints WHERE project_id=?`, project, project, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return nil, err
		}
		texts = append(texts, text)
	}
	return texts, rows.Err()
}

// Correct previously saved model-derived ports before restarting a monitor.
// A new run ID invalidates in-flight checks against the incorrect endpoints.
func (d *DB) ReconcileTargetProbeURLs(ctx context.Context, project string, scope ...string) (TargetProbeState, error) {
	var state TargetProbeState
	err := tx(ctx, d, func(t *sql.Tx) error {
		var err error
		state, err = readTargetProbe(t.QueryRow(`SELECT `+targetProbeSelect+` FROM project_target_health WHERE project_id=?`, project))
		if err != nil || state.Status != "ready" || state.Analysis == nil || !state.Analysis.Required {
			return err
		}
		texts, err := targetProbeSources(t, project)
		if err != nil {
			return err
		}
		resolved, err := resolveTargetProbeURLPorts(*state.Analysis, append(texts, scope...)...)
		if err != nil {
			// Ambiguous old output needs a fresh analysis using the parsed URLs.
			_, err = t.Exec(`UPDATE project_target_health SET run_id=lower(hex(randomblob(16))),preprocessing_json='{"status":"pending"}',status='pending',analysis_json='',failure_counts_json='[]',last_checked_at='',last_error='',analysis_failures=0,retry_after='' WHERE project_id=?`, project)
		} else if !reflect.DeepEqual(resolved.Targets, state.Analysis.Targets) {
			_, err = t.Exec(`UPDATE project_target_health SET run_id=lower(hex(randomblob(16))),preprocessing_json='{"status":"pending"}',analysis_json=?,failure_counts_json=?,last_checked_at='',last_error='' WHERE project_id=?`, jsonData(resolved), jsonData(make([]int, len(resolved.Targets))), project)
		} else {
			return nil
		}
		if err != nil {
			return err
		}
		state, err = readTargetProbe(t.QueryRow(`SELECT `+targetProbeSelect+` FROM project_target_health WHERE project_id=?`, project))
		return err
	})
	return state, err
}
