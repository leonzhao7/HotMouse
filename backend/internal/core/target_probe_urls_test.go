package core

import (
	"reflect"
	"testing"
)

const numericHostnameOrigin = "CTF 挑战题，类别为 [web]，附件在/tmp/author/ae7f901f70e6d0aba59666d7de981b24.zip，目标是[http://oj-10-30-15-1-54549.adworld.xctf.org.cn/]和[http://oj-10-30-15-1-37669.adworld.xctf.org.cn/]"

func TestTargetProbeURLCandidatesUsesURLAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want         []TargetEndpoint
	}{
		{"numeric_hostnames", numericHostnameOrigin, []TargetEndpoint{{Host: "oj-10-30-15-1-54549.adworld.xctf.org.cn", Port: 80}, {Host: "oj-10-30-15-1-37669.adworld.xctf.org.cn", Port: 80}}},
		{"https_default", "目标[https://ctf-31337.example/54549]", []TargetEndpoint{{Host: "ctf-31337.example", Port: 443}}},
		{"explicit_port", "[http://ctf-31337.example:8080/54549]", []TargetEndpoint{{Host: "ctf-31337.example", Port: 8080}}},
		{"ipv6_default", "[http://[::1]/]", []TargetEndpoint{{Host: "::1", Port: 80}}},
		{"ipv6_explicit", "[https://[2001:db8::1]:8443/54549]", []TargetEndpoint{{Host: "2001:db8::1", Port: 8443}}},
		{"credentials", "http://user:password@ctf.example:8080/", []TargetEndpoint{{Host: "ctf.example", Port: 8080}}},
		{"duplicate_urls", "http://ctf.example/ [http://CTF.EXAMPLE:80/path]", []TargetEndpoint{{Host: "ctf.example", Port: 80}}},
		{"raw_tcp", "pwn target ctf.example:31337", nil},
		{"invalid_explicit_port", "http://ctf.example:unknown/", nil},
		{"port_out_of_range", "http://ctf.example:65536/", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TargetProbeURLCandidates(tc.source); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("URL candidates=%+v want=%+v", got, tc.want)
			}
		})
	}
}

func TestTargetProbeURLPortResolutionPreservesExplicitServices(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		port, want   int
		wantError    bool
	}{
		{"http_hostname_digits", "http://ctf-31337.example/", 31337, 80, false},
		{"https_hostname_digits", "https://ctf-31337.example/", 31337, 443, false},
		{"explicit_url", "http://ctf-31337.example:8080/", 31337, 8080, false},
		{"separate_tcp_service", "HTTP target http://ctf-31337.example/; pwn target ctf-31337.example:31337", 31337, 31337, false},
		{"raw_tcp", "ctf-31337.example:31337", 31337, 31337, false},
		{"ambiguous_wrong_port", "http://ctf-31337.example/ https://ctf-31337.example/", 31337, 0, true},
		{"multiple_explicit_ports", "http://ctf-31337.example:8080/ http://ctf-31337.example:9090/", 9090, 9090, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			analysis := TargetProbeAnalysis{Required: true, Reason: "remote CTF", Targets: []TargetEndpoint{{Host: "ctf-31337.example", Port: tc.port}}}
			got, err := resolveTargetProbeURLPorts(analysis, tc.source)
			if (err != nil) != tc.wantError {
				t.Fatalf("resolution err=%v wantError=%v", err, tc.wantError)
			}
			if !tc.wantError && got.Targets[0].Port != tc.want {
				t.Fatalf("port=%d want=%d", got.Targets[0].Port, tc.want)
			}
			if analysis.Targets[0].Port != tc.port {
				t.Fatal("resolution changed caller's original model response")
			}
		})
	}
}

func TestTargetProbeCorrectsNewAndPersistedNumericHostnamePorts(t *testing.T) {
	db := testDB(t)
	p := strategyProject(t, db)
	if _, err := db.SQL.Exec(`UPDATE facts SET description=? WHERE project_id=? AND id='origin'`, numericHostnameOrigin, p); err != nil {
		t.Fatal(err)
	}
	wrong := TargetProbeAnalysis{Required: true, Reason: "remote CTF", Targets: []TargetEndpoint{{Host: "oj-10-30-15-1-54549.adworld.xctf.org.cn", Port: 54549}, {Host: "oj-10-30-15-1-37669.adworld.xctf.org.cn", Port: 37669}}}
	state := readyTargetProbe(t, db, p, wrong.Targets...)
	if len(state.Analysis.Targets) != 2 || state.Analysis.Targets[0].Port != 80 || state.Analysis.Targets[1].Port != 80 {
		t.Fatalf("model's hostname-derived ports accepted: %+v", state.Analysis)
	}
	// Simulate a plan saved by the previous build, about to fail on round three.
	if _, err := db.SQL.Exec(`UPDATE project_target_health SET analysis_json=?,failure_counts_json='[2,2]',last_checked_at=?,last_error='incorrect port refused' WHERE project_id=?`, jsonData(wrong), now(), p); err != nil {
		t.Fatal(err)
	}
	corrected, err := db.ReconcileTargetProbeURLs(t.Context(), p)
	if err != nil || corrected.RunID == state.RunID || corrected.Status != "ready" || corrected.Analysis.Targets[0].Port != 80 || corrected.Analysis.Targets[1].Port != 80 || !reflect.DeepEqual(corrected.ConsecutiveFailures, []int{0, 0}) || corrected.LastCheckedAt != "" || corrected.LastError != "" {
		t.Fatalf("persisted wrong ports or counters retained: %+v err=%v", corrected, err)
	}
	if failed, err := db.RecordTargetProbe(t.Context(), p, state.RunID, []string{"refused", "refused"}); err != nil || failed {
		t.Fatalf("old monitor's incorrect port checks failed the project: failed=%v err=%v", failed, err)
	}
	again, err := db.ReconcileTargetProbeURLs(t.Context(), p)
	if err != nil || again.RunID != corrected.RunID {
		t.Fatalf("corrected plan was repeatedly reset: %+v err=%v", again, err)
	}
}
