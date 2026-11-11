package worker

import (
	"context"
	"fmt"
	"time"
)

// One background startup per project run owns analysis and web preprocessing.
// Its provider workspace and lifetime are independent of the planning lease.
func (r *Runner) startTargetStartup(parent context.Context, project string) {
	meta, err := r.DB.GetProjectMeta(parent, project)
	if err != nil || meta.Status != "active" {
		return
	}
	state, err := r.DB.TargetProbe(parent, project)
	if err != nil {
		return
	}
	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	if r.lifecycleCtx != nil {
		parent = r.lifecycleCtx
	}
	if parent.Err() != nil {
		return
	}
	previous := r.targetStartups[project]
	if previous != nil {
		if previous.runID == state.RunID {
			select {
			case <-previous.done:
				if state.Status == "ready" && state.Analysis != nil && state.Preprocessing.Status != "pending" {
					if !state.Analysis.Required {
						return
					}
					if monitor := r.targetMonitors[project]; monitor != nil && monitor.runID == state.RunID {
						select {
						case <-monitor.done:
						default:
							return
						}
					}
				}
			default:
				return
			}
		}
		previous.cancel()
	}
	ctx, finishProject := r.projectContext(parent, project)
	ctx, cancel := context.WithCancel(ctx)
	startup := &targetMonitor{runID: state.RunID, cancel: cancel, done: make(chan struct{})}
	r.targetStartups[project] = startup
	go func() {
		defer close(startup.done)
		defer finishProject()
		defer cancel()
		if previous != nil {
			<-previous.done
		}
		checkRun := func() error {
			meta, err := r.DB.GetProjectMeta(ctx, project)
			if err != nil {
				return err
			}
			current, err := r.DB.TargetProbe(ctx, project)
			if err != nil {
				return err
			}
			if meta.Status != "active" || current.RunID != startup.runID {
				return fmt.Errorf("target startup no longer belongs to the active project run")
			}
			return nil
		}
		stop := r.heartbeat(ctx, cancel, checkRun)
		defer stop()
		interval := r.Interval
		if interval <= 0 {
			interval = time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for checkRun() == nil {
			if ready, _ := r.ensureStartupWithMonitor(ctx, parent, project); ready {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
