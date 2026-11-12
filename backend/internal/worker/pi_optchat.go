package worker

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

//go:embed pi_optchat_owner.mjs
var piOptchatOwnerExtension string

type piOptchatConfig struct {
	profile, home, plugin, runtime, socket string
}

type piOptchatOwner struct {
	project string
	cancel  context.CancelFunc
	ready   chan struct{}
	done    chan struct{}
	err     error
}

var piOptchatProfileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (r *Runner) piOptchatProfile(project string) string {
	digest := sha256.Sum256([]byte(r.DB.WorkspaceMarker(project)))
	name := fmt.Sprintf("hotmouse-%s-%x", project, digest[:6])
	if !piOptchatProfileName.MatchString(name) {
		name = fmt.Sprintf("hotmouse-%x", digest[:16])
	}
	return name
}

// Discover the installed package before PI_CODING_AGENT_DIR is redirected to
// this task's isolated model configuration. An explicit empty value disables it.
func installedPiOptchat(cfg workerConfig) (string, error) {
	if path, configured := cfg.env["PI_OPTCHAT_EXTENSION"]; configured {
		if path == "" {
			return "", nil
		}
		path, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("PI_OPTCHAT_EXTENSION must name the installed pi-optchat src/index.ts")
		}
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, dir := range []string{configEnvironment(cfg, "PI_CODING_AGENT_DIR"), filepath.Join(home, ".pi", "agent")} {
		if dir == "" {
			continue
		}
		path := filepath.Join(dir, "npm", "node_modules", "pi-optchat", "src", "index.ts")
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode().IsRegular() {
			return filepath.Abs(path)
		}
	}
	return "", nil
}

func (r *Runner) preparePiOptchat(ctx context.Context, cfg workerConfig, vars map[string]string) (*piOptchatConfig, error) {
	if canonicalDriver(cfg.driver) != "pi" || vars["HOTMOUSE_PROJECT_ID"] == "" {
		return nil, nil
	}
	plugin, err := installedPiOptchat(cfg)
	if err != nil || plugin == "" {
		return nil, err
	}
	project := vars["HOTMOUSE_PROJECT_ID"]
	directory := vars["HOTMOUSE_PROJECT_DIR"]
	if directory == "" {
		return nil, fmt.Errorf("Pi OptChat requires the project workspace")
	}
	home := configEnvironment(cfg, "OPTCHAT_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".optchat")
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return nil, err
	}
	profile := r.piOptchatProfile(project)
	data, err := json.Marshal(map[string]string{"profile": profile, "home": home})
	if err != nil {
		return nil, err
	}
	if err := writeWorkflowFile(filepath.Join(directory, ".hotmouse", "optchat-profile.json"), data); err != nil {
		return nil, err
	}
	runtime := filepath.Join(directory, ".hotmouse", "optchat", profile)
	if err := os.MkdirAll(runtime, 0o700); err != nil {
		return nil, err
	}
	profileDirectory := filepath.Join(home, "profiles", profile)
	config := &piOptchatConfig{profile: profile, home: home, plugin: plugin, runtime: runtime,
		socket: filepath.Join(profileDirectory, "windows.sock")}
	vars["OPTCHAT_HOME"] = home
	if err := r.ensurePiOptchatOwner(ctx, project, cfg, config); err != nil {
		return nil, err
	}
	return config, nil
}

func piOptchatAvailable(ctx context.Context, config *piOptchatConfig) bool {
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	connection, err := dialer.DialContext(ctx, "unix", config.socket)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func (r *Runner) ensurePiOptchatOwner(ctx context.Context, project string, cfg workerConfig, config *piOptchatConfig) error {
	if piOptchatAvailable(ctx, config) {
		return nil
	}
	r.optchatMu.Lock()
	owner := r.optchatOwners[config.profile]
	if owner == nil {
		parent := context.Background()
		r.probeMu.Lock()
		if r.lifecycleCtx != nil {
			parent = r.lifecycleCtx
		}
		r.probeMu.Unlock()
		ownerCtx, cancel := context.WithCancel(parent)
		owner = &piOptchatOwner{project: project, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
		if r.optchatOwners == nil {
			r.optchatOwners = map[string]*piOptchatOwner{}
		}
		r.optchatOwners[config.profile] = owner
		go r.startPiOptchatOwner(ownerCtx, cfg, config, owner)
	}
	r.optchatMu.Unlock()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-owner.done:
			return fmt.Errorf("Pi OptChat profile owner stopped: %w", owner.err)
		case <-owner.ready:
			return nil
		case <-ticker.C:
			if piOptchatAvailable(ctx, config) {
				return nil
			}
		}
	}
}

func (r *Runner) startPiOptchatOwner(ctx context.Context, cfg workerConfig, config *piOptchatConfig, owner *piOptchatOwner) {
	defer func() {
		close(owner.done)
		r.optchatMu.Lock()
		if r.optchatOwners[config.profile] == owner {
			delete(r.optchatOwners, config.profile)
		}
		r.optchatMu.Unlock()
	}()
	startupCtx, cancelStartup := context.WithTimeout(ctx, 60*time.Second)
	defer cancelStartup()
	lock := flock.New(filepath.Join(config.runtime, "startup.lock"), flock.SetPermissions(0o600))
	locked, err := lock.TryLockContext(startupCtx, 50*time.Millisecond)
	if err != nil || !locked {
		owner.err = fmt.Errorf("initialize Pi OptChat: %w", err)
		return
	}
	defer lock.Close()
	if piOptchatAvailable(startupCtx, config) {
		close(owner.ready)
		<-ctx.Done()
		owner.err = ctx.Err()
		return
	}
	ownerPath := filepath.Join(config.runtime, "owner.mjs")
	profilesPath := filepath.Join(filepath.Dir(config.plugin), "profiles.ts")
	extension := strings.NewReplacer(
		"__OPTCHAT_INDEX__", quotedJS(config.plugin),
		"__OPTCHAT_PROFILES__", quotedJS(profilesPath),
		"__OPTCHAT_PROFILE__", quotedJS(config.profile),
	).Replace(piOptchatOwnerExtension)
	if err := writeWorkflowFile(ownerPath, []byte(extension)); err != nil {
		owner.err = err
		return
	}
	argv, err := providerArgv(cfg, "decide", "", config.runtime, "")
	if err != nil {
		owner.err = err
		return
	}
	argv = argv[:len(argv)-2]
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--mode" {
			argv[i+1] = "rpc"
			break
		}
	}
	argv = append(argv, "-e", ownerPath, "--optchat-profile", config.profile)
	vars := map[string]string{"OPTCHAT_HOME": config.home}
	if cfg.env["PI_MODEL"] != "" && cfg.env["PI_BASE_URL"] != "" && cfg.env["PI_API_KEY"] != "" && cfg.env["PI_PROVIDER_API"] != "" {
		vars["PI_CODING_AGENT_DIR"] = filepath.Join(config.runtime, ".hotmouse", "pi-agent")
	}
	processCtx, cancelProcess := context.WithCancel(ctx)
	defer cancelProcess()
	input, inputWriter, err := os.Pipe()
	if err != nil {
		owner.err = err
		return
	}
	defer input.Close()
	defer inputWriter.Close()
	finished := make(chan processResult, 1)
	go func() {
		finished <- runProcessWithInput(processCtx, argv, cfg.env, config.runtime, vars, 0, input, nil, nil, r.processLogger(cfg, owner.project, "", "pi_optchat_owner"))
	}()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case result := <-finished:
			if result.err != nil {
				owner.err = fmt.Errorf("start Pi OptChat profile owner: %w", result.err)
			} else {
				owner.err = fmt.Errorf("Pi OptChat profile owner exited before starting")
			}
			return
		case <-startupCtx.Done():
			cancelProcess()
			<-finished
			owner.err = startupCtx.Err()
			return
		case <-poll.C:
			if piOptchatAvailable(startupCtx, config) {
				close(owner.ready)
				_ = lock.Close()
				result := <-finished
				if result.err != nil {
					owner.err = fmt.Errorf("Pi OptChat profile owner exited: %w", result.err)
				} else {
					owner.err = fmt.Errorf("Pi OptChat profile owner exited")
				}
				return
			}
		}
	}
}

func piOptchatPrintArgs(argv []string, config *piOptchatConfig, prompt string) []string {
	args := append([]string(nil), argv[:1]...)
	for i := 1; i < len(argv); i++ {
		switch argv[i] {
		case "--mode", "--session":
			i++
		case "-p":
			i++
		default:
			args = append(args, argv[i])
		}
	}
	return append(args, "-e", config.plugin, "--optchat-profile", config.profile, "--optchat-connect", "auto", "-p", prompt)
}

func quotedJS(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func (r *Runner) stopPiOptchatOwners(project string) {
	r.optchatMu.Lock()
	for _, owner := range r.optchatOwners {
		if project == "" || owner.project == project {
			owner.cancel()
		}
	}
	r.optchatMu.Unlock()
}
