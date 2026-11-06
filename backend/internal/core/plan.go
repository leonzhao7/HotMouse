package core

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type PlanStepUpdate struct {
	ID     string
	Change UpdateStep
}
type PlanRouteUpdate struct {
	ID     string
	Change UpdateRoute
}
type DecisionPlan struct {
	Steps        []CreateStep
	StepUpdates  []PlanStepUpdate
	Goals        []CreateGoal
	RouteUpdates []PlanRouteUpdate
}

// ApplyDecisionPlan prevents a rejected later update from leaving earlier
// mutations committed. Leases and recovery checkpoints are checked in the same transaction.
func (d *DB) ApplyDecisionPlan(ctx context.Context, project, worker string, plan DecisionPlan) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		if _, err := checkActive(t, project); err != nil {
			return err
		}
		if err := expireDecideLeaseTx(t, project); err != nil {
			return err
		}
		var owner sql.NullString
		if err := t.QueryRow(`SELECT decide_worker FROM projects WHERE id=?`, project).Scan(&owner); err != nil {
			return err
		}
		if owner.Valid && owner.String != worker {
			return fmt.Errorf("project decide is currently claimed by %s", owner.String)
		}
		for _, u := range plan.RouteUpdates {
			if u.Change.Status != nil && *u.Change.Status == "active" {
				var reason string
				if err := t.QueryRow(`SELECT shelved_reason FROM routes WHERE project_id=? AND id=?`, project, u.ID).Scan(&reason); err != nil {
					return err
				}
				if reason == "needs_attention" {
					return fmt.Errorf("route requires manual review before reopening")
				}
			}
			var out Route
			if err := d.updateRouteTx(ctx, t, project, u.ID, u.Change, &out, false); err != nil {
				return err
			}
			var status string
			if err := t.QueryRow(`SELECT status FROM projects WHERE id=?`, project).Scan(&status); err != nil {
				return err
			}
			// Shelving is terminal. Commit the failure and preserve its evidence;
			// later step or goal mutations must not roll it back or start new work.
			if status == "failed" {
				return nil
			}
		}
		if err := validatePlanStepHistory(t, project, plan.Steps); err != nil {
			return err
		}
		for _, u := range plan.StepUpdates {
			// The model works from a snapshot. A worker may claim or finish a
			// step while planning; preserve that work and apply the rest of the plan.
			var immutable bool
			if err := t.QueryRow(`SELECT worker IS NOT NULL OR to_fact_id IS NOT NULL OR abandoned<>0 OR EXISTS(SELECT 1 FROM worker_execute_recovery x WHERE x.project_id=steps.project_id AND x.step_id=steps.id) FROM steps WHERE project_id=? AND id=?`, project, u.ID).Scan(&immutable); err != nil {
				return err
			}
			if immutable {
				continue
			}
			var out Step
			if err := d.updateStepTx(ctx, t, project, u.ID, u.Change, &out); err != nil {
				return err
			}
		}
		for _, g := range plan.Goals {
			var out Goal
			if err := d.createGoalTx(ctx, t, project, g, &out); err != nil {
				return err
			}
		}
		for _, s := range plan.Steps {
			var out Step
			if err := d.createStepTx(ctx, t, project, s, &out); err != nil {
				return err
			}
		}
		return nil
	})
}

func validatePlanStepHistory(t *sql.Tx, project string, steps []CreateStep) error {
	if len(steps) == 0 {
		return nil
	}
	key := func(description string) string {
		return strings.ToLower(strings.Join(strings.Fields(description), " "))
	}
	seen := map[string]string{}
	rows, err := t.Query(`SELECT id,description FROM steps WHERE project_id=?`, project)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, description string
		if err := rows.Scan(&id, &description); err != nil {
			rows.Close()
			return err
		}
		seen[key(description)] = id
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for i, step := range steps {
		if err := requireText("description", step.Description); err != nil {
			return err
		}
		identity := key(step.Description)
		if previous, ok := seen[identity]; ok {
			return fmt.Errorf("planned step %d duplicates %s; choose a different action from all historical steps, regardless of status", i+1, previous)
		}
		seen[identity] = fmt.Sprintf("planned step %d", i+1)
	}
	return nil
}
