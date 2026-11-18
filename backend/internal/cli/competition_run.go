package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
	"hotmouse/backend/internal/competition"
	"hotmouse/backend/internal/core"
	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/ctfplatform/tsec"
	"hotmouse/backend/internal/httpapi"
	"hotmouse/backend/internal/logging"
)

const unattendedTSecRules = `Run this TSec Benchmark task autonomously. No operator can answer questions, approve steps, provide hints or repair the run after startup.
Solve every challenge returned for this benchmark task, using only the delivered challenge services and files as target scope. The scoring platform and VPN infrastructure are not challenge targets. The runtime already provides the platform VPN network.
Use HotMouse's Dispatcher, execution Workers, specialist skills, shared memory and offline references. Save confirmed flags as flag findings or checkpoints with their raw value, challenge_id and source evidence. A challenge may require several flags; continue until every remaining flag is accepted by the platform.
The deterministic coordinator starts and closes instances, polls platform progress and submits saved flags. Do not call platform submission, start, close or hint endpoints from a model conversation. Hints cost score and are disabled for this run.
Reuse saved evidence and workspaces across retries. Wrong answers require a concrete correction. The coordinator handles interrupted submissions through TSec's duplicate-safe API, and retries failed challenges. Never wait for human input; preserve useful evidence and let automatic recovery or the next solving attempt proceed.`

func (a *app) competitionRunCommand() *cobra.Command {
	c := a.leaf("run <PLATFORM>", "直接运行无人值守比赛；无需服务、登录或规则文件", 1, 1, "none", a.runCompetition)
	c.Annotations["local"] = "true"
	c.Flags().String("db", env("HOTMOUSE_DB", "hotmouse.db"), "本地 SQLite 文件及持久化比赛状态")
	c.Flags().String("rules-file", env("HOTMOUSE_COMPETITION_RULES_FILE", ""), "可选规则文件；省略时使用平台默认规则")
	c.Flags().Int("concurrency", 0, "同时解答题目数；默认使用系统配置，TSec 最大 3")
	c.Flags().Int("max-workers", 0, "每题 Worker 上限；默认使用系统配置")
	c.Flags().Duration("challenge-timeout", 20*time.Minute, "每题从启动到完成的总时限")
	c.Flags().Duration("retry-interval", 5*time.Second, "平台启动失败后的自动重试间隔")
	c.Flags().Duration("shutdown-timeout", 30*time.Second, "停止 Worker 并回收靶机的最长时间")
	c.Flags().String("result-file", env("TSEC_RESULT_FILE", ""), "比赛结果 JSON 文件；默认保存在数据库旁")
	c.Example = "  hotmouse competition run tsec --db ./data/tsec.db"
	return c
}

func (a *app) runCompetition(c *cobra.Command, args []string) (result any, runErr error) {
	if args[0] != "tsec" {
		return nil, invalid("unattended competition currently supports tsec")
	}
	platformConfig := ctfplatform.Config{BaseURL: os.Getenv("BENCHMARK_BASE_URL"), Token: os.Getenv("BENCHMARK_TOKEN")}
	if _, err := tsec.New(platformConfig); err != nil {
		return nil, invalid("TSec configuration: %s", err)
	}
	if mode := os.Getenv("TSEC_VPN_MODE"); mode != "" && mode != "external" {
		return nil, invalid("TSEC_VPN_MODE must be external; this runtime uses the VPN already connected by its host")
	}
	probeSetting := os.Getenv("TSEC_TARGET_PROBE_ENABLED")
	if probeSetting != "" && probeSetting != "0" && probeSetting != "1" {
		return nil, invalid("TSEC_TARGET_PROBE_ENABLED must be 0 or 1")
	}
	probeEnabled := probeSetting == "1"
	networkCheck := os.Getenv("TSEC_NETWORK_CHECK_URL")
	if networkCheck != "" {
		u, err := url.Parse(networkCheck)
		if err != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Fragment != "" {
			return nil, invalid("TSEC_NETWORK_CHECK_URL must be an HTTP(S) URL without credentials or fragment")
		}
	}
	path, _ := c.Flags().GetString("db")
	if path == "" {
		return nil, invalid("db must be a file path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, invalid("db: %s", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, failed("create database directory: %s", err)
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	} else if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(real, filepath.Base(path))
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0600))
	held, err := lock.TryLock()
	if err != nil {
		return nil, failed("database lock: %s", err)
	}
	defer lock.Close()
	if !held {
		return nil, failed("another HotMouse process owns this database")
	}
	db, err := core.Open(path)
	if err != nil {
		return nil, failed("open database: %s", err)
	}
	defer db.SQL.Close()
	logger := logging.New(a.errOut, "tsec-agent")
	if a.verbose {
		logger.SetLevel(slog.LevelDebug)
	}
	logger.Runtime()
	if _, err := db.ApplyServerEnvironment(c.Context()); err != nil {
		return nil, invalid("model environment: %s", err)
	}
	if _, err := db.SQL.ExecContext(c.Context(), `UPDATE worker_config SET enabled=1 WHERE id=1`); err != nil {
		return nil, failed("enable competition workers: %s", err)
	}
	if err := db.ValidateAutonomousWorker(c.Context()); err != nil {
		return nil, invalid("model configuration: %s", err)
	}
	if _, err := exec.LookPath("pi"); err != nil {
		return nil, failed("native Pi CLI is unavailable: %s", err)
	}
	config, retry, shutdown, err := a.unattendedCompetitionConfig(c, db)
	if err != nil {
		return nil, err
	}
	scope := platformConfig.ScopeID("tsec")
	// A standalone run shares its worker pool with this task only. Separate
	// volumes are required when another live task owns saved active projects.
	var unrelated bool
	if err := db.SQL.QueryRowContext(c.Context(), `SELECT EXISTS(SELECT 1 FROM projects p LEFT JOIN platform_projects b ON b.project_id=p.id WHERE p.status='active' AND (b.platform IS NULL OR b.platform<>'tsec' OR b.scope_id<>?))`, scope).Scan(&unrelated); err != nil {
		return nil, err
	}
	if unrelated {
		return nil, failed("database contains active projects outside this benchmark task; use a dedicated TSec data volume")
	}
	if err := db.RecoverPendingTSecSubmissions(c.Context(), scope); err != nil {
		return nil, failed("recover submissions: %s", err)
	}
	handler := httpapi.New(db)
	handler.PlatformOverrides = map[string]ctfplatform.Config{"tsec": platformConfig}
	handler.Runner.SetLogger(logger)
	handler.Runner.SetTargetProbesEnabled(probeEnabled)
	handler.Competitions.Logger = logger.WithComponent("competition")
	handler.Competitions.Unattended = true
	handler.Competitions.Platform, handler.Competitions.ScopeID = "tsec", scope
	// All original rules reach each project. Initial ordering needs no model
	// round trip, so a missing rule interpreter cannot block an unattended run.
	handler.Competitions.Backend.Plan = unattendedTSecPlan
	lifecycle, cancel := context.WithCancel(context.WithoutCancel(c.Context()))
	handler.Runner.SetLifecycleContext(lifecycle)
	competitionDone := handler.Competitions.Start(lifecycle)
	var workerDone <-chan struct{}
	var run core.CompetitionRun
	resultFile, _ := c.Flags().GetString("result-file")
	if resultFile == "" {
		resultFile = filepath.Join(filepath.Dir(path), "tsec-result.json")
	}
	defer func() {
		cleanup, finish := context.WithTimeout(context.Background(), shutdown)
		defer finish()
		if run.ID != "" {
			current, err := db.GetCompetitionRun(cleanup, run.ID)
			if err == nil && !competitionFinished(current.Status) {
				if err := handler.Competitions.Stop(cleanup, run.ID); err != nil {
					logger.Error("competition.stop_failed", "error", err)
				} else if _, err := waitCompetition(cleanup, db, run.ID, 100*time.Millisecond); err != nil {
					logger.Error("competition.cleanup_timeout", "error", err)
					if runErr == nil {
						runErr = err
					}
				}
			}
		}
		cancel()
		handler.Runner.StopTargetProbes()
		for _, done := range []<-chan struct{}{competitionDone, workerDone} {
			if done == nil {
				continue
			}
			select {
			case <-done:
			case <-cleanup.Done():
				if runErr == nil {
					runErr = failed("competition shutdown timed out")
				}
			}
		}
		if run.ID != "" {
			// Reporting has its own budget even after a slow resource cleanup.
			reportCtx, finishReport := context.WithTimeout(context.Background(), 5*time.Second)
			defer finishReport()
			if current, err := db.GetCompetitionRun(reportCtx, run.ID); err == nil {
				run, result = current, current
				if err := saveCompetitionResult(resultFile, current); err != nil {
					logger.Error("competition.report_failed", "error", err)
					if runErr == nil {
						runErr = failed("save competition result: %s", err)
					}
				}
			}
		}
	}()
	logger.Info("competition.autonomous.start", "platform", "tsec", "db", path, "vpn_mode", "external", "network_check", networkCheck, "target_probe_enabled", probeEnabled)
	for {
		if c.Context().Err() != nil {
			return nil, c.Context().Err()
		}
		if networkCheck != "" {
			err = checkCompetitionNetwork(c.Context(), networkCheck)
		}
		if err == nil {
			run, err = handler.ResumeOrStartCompetition(c.Context(), "tsec", config)
		}
		if err == nil {
			break
		}
		if !retryCompetitionStartup(err) {
			return nil, failed("start competition: %s", err)
		}
		logger.Warn("competition.start.retry", "error", err, "retry_in", retry)
		if err := waitCompetitionRetry(c.Context(), retry); err != nil {
			return nil, err
		}
		err = nil
	}
	logger.Info("competition.autonomous.ready", "run", run.ID, "challenges", len(run.Items), "concurrency", run.Concurrency, "max_workers", run.MaxWorkers)
	if !competitionFinished(run.Status) {
		workerDone = handler.Runner.Start(lifecycle)
		current, waitErr := waitCompetition(c.Context(), db, run.ID, time.Second)
		if current.ID != "" {
			run = current
		}
		err = waitErr
		if err != nil {
			return run, err
		}
	}
	logger.Info("competition.autonomous.finished", "run", run.ID, "status", run.Status, "result_file", resultFile)
	if run.Status == "failed" {
		return run, failed("competition ended: %s", run.LastError)
	}
	return run, nil
}

func (a *app) unattendedCompetitionConfig(c *cobra.Command, db *core.DB) (core.CompetitionConfig, time.Duration, time.Duration, error) {
	config := core.CompetitionConfig{Rules: unattendedTSecRules}
	if file, _ := c.Flags().GetString("rules-file"); file != "" {
		data, err := a.readFile(file)
		if err != nil {
			return config, 0, 0, err
		}
		config.Rules += "\nAdditional competition rules:\n" + string(data)
	}
	for _, setting := range []struct {
		flag, environment, query string
		target                   *int
	}{
		{"concurrency", "HOTMOUSE_COMPETITION_CONCURRENCY", `SELECT project_concurrency FROM worker_config WHERE id=1`, &config.Concurrency},
		{"max-workers", "HOTMOUSE_COMPETITION_MAX_WORKERS", `SELECT default_project_max_workers FROM settings WHERE id=1`, &config.MaxWorkers},
	} {
		value, _ := c.Flags().GetInt(setting.flag)
		if !c.Flags().Changed(setting.flag) {
			if raw := os.Getenv(setting.environment); raw != "" {
				var err error
				value, err = strconv.Atoi(raw)
				if err != nil || value < 1 {
					return config, 0, 0, invalid("%s must be a positive integer", setting.environment)
				}
			} else {
				if err := db.SQL.QueryRowContext(c.Context(), setting.query).Scan(&value); err != nil {
					return config, 0, 0, err
				}
				if setting.flag == "concurrency" {
					value = min(3, max(1, value))
				}
			}
		} else if value < 1 {
			return config, 0, 0, invalid("--%s must be a positive integer", setting.flag)
		}
		*setting.target = value
	}
	if config.Concurrency > 3 {
		return config, 0, 0, invalid("TSec allows at most 3 concurrent challenges")
	}
	durations := []struct{ flag, environment string }{
		{"challenge-timeout", "HOTMOUSE_COMPETITION_CHALLENGE_TIMEOUT"},
		{"retry-interval", "TSEC_RETRY_INTERVAL"},
		{"shutdown-timeout", "TSEC_SHUTDOWN_TIMEOUT"},
	}
	values := make([]time.Duration, len(durations))
	for i, setting := range durations {
		value, _ := c.Flags().GetDuration(setting.flag)
		if raw := os.Getenv(setting.environment); raw != "" && !c.Flags().Changed(setting.flag) {
			var err error
			value, err = time.ParseDuration(raw)
			if err != nil {
				return config, 0, 0, invalid("%s must be a duration", setting.environment)
			}
		}
		if value <= 0 || value > 24*time.Hour {
			return config, 0, 0, invalid("--%s must be positive and at most 24h", setting.flag)
		}
		values[i] = value
	}
	config.ChallengeTimeoutSeconds = int(values[0] / time.Second)
	if config.ChallengeTimeoutSeconds < 1 {
		return config, 0, 0, invalid("--challenge-timeout must be at least 1s")
	}
	if err := config.Normalize(); err != nil {
		return config, 0, 0, invalid("%s", err)
	}
	return config, values[1], values[2], nil
}

func unattendedTSecPlan(_ context.Context, run core.CompetitionRun, challenges []ctfplatform.Challenge) (competition.Plan, error) {
	byID := make(map[string]ctfplatform.Challenge, len(challenges))
	for _, challenge := range challenges {
		byID[challenge.ID] = challenge
	}
	ids := make([]string, 0, len(run.Items))
	for _, item := range run.Items {
		ids = append(ids, item.ChallengeID)
	}
	priority := func(challenge ctfplatform.Challenge) [3]int {
		status := 3
		switch challenge.InstanceStatus {
		case "available":
			status = 0
		case "pending":
			status = 1
		case "stop_pending":
			status = 2
		}
		difficulty := 3
		switch strings.ToLower(challenge.Difficulty) {
		case "easy":
			difficulty = 0
		case "medium":
			difficulty = 1
		case "hard":
			difficulty = 2
		}
		return [3]int{status, difficulty, challenge.Level}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		left, right := priority(byID[ids[i]]), priority(byID[ids[j]])
		for k := range left {
			if left[k] != right[k] {
				return left[k] < right[k]
			}
		}
		return false
	})
	return competition.Plan{Summary: "Autonomous TSec: recover existing instances first, then solve easier challenges; apply all original rules and submit every verified flag", ChallengeIDs: ids}, nil
}

func competitionFinished(status string) bool {
	return status == "completed" || status == "stopped" || status == "failed"
}

func waitCompetition(ctx context.Context, db *core.DB, id string, interval time.Duration) (core.CompetitionRun, error) {
	for {
		run, err := db.GetCompetitionRun(ctx, id)
		if err != nil || competitionFinished(run.Status) {
			return run, err
		}
		if err := waitCompetitionRetry(ctx, interval); err != nil {
			return run, err
		}
	}
}

func waitCompetitionRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryCompetitionStartup(err error) bool {
	var failure *ctfplatform.Error
	return errors.As(err, &failure) && (failure.HTTPStatus >= 500 || failure.HTTPStatus == http.StatusTooManyRequests)
}

func checkCompetitionNetwork(ctx context.Context, address string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	// VPN health endpoints must use the provided network, independent of proxies.
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ctfplatform.Error{HTTPStatus: 503, Code: "vpn_not_ready", Message: "VPN health endpoint is unreachable"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ctfplatform.Error{HTTPStatus: 503, Code: "vpn_not_ready", Message: fmt.Sprintf("VPN health endpoint returned HTTP %d", response.StatusCode)}
	}
	return nil
}

func saveCompetitionResult(path string, run core.CompetitionRun) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".tsec-result-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
