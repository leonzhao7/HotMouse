package tpctf

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type challengeState struct {
	InstanceID string    `json:"instance_id,omitempty"`
	Closing    bool      `json:"closing,omitempty"`
	RetryAt    time.Time `json:"retry_at,omitempty"`
	// Journal before POST: a lost response must not spend a second attempt.
	Submitted map[string]string `json:"submitted,omitempty"`
}
type authorState struct {
	Slug        string    `json:"slug"`
	ID          string    `json:"id,omitempty"`
	Uploads     int       `json:"uploads"`
	RetryAt     time.Time `json:"retry_at,omitempty"`
	ArchiveHash string    `json:"archive_hash,omitempty"`
	Pending     bool      `json:"pending,omitempty"`
}
type state struct {
	CompetitionID string                     `json:"competition_id"`
	BaseURL       string                     `json:"base_url"`
	Challenges    map[string]*challengeState `json:"challenges"`
	Authors       map[string]*authorState    `json:"authors"`
	PauseUntil    time.Time                  `json:"pause_until,omitempty"`
}

func loadState(dir, cid, base string) (*state, error) {
	s := &state{CompetitionID: cid, BaseURL: base, Challenges: map[string]*challengeState{}, Authors: map[string]*authorState{}}
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("invalid persisted competition state: %w", err)
	}
	if s.CompetitionID != cid || s.BaseURL != base {
		return nil, fmt.Errorf("work directory belongs to another competition/API; use a separate CTFY_WORK_DIR")
	}
	if s.Challenges == nil {
		s.Challenges = map[string]*challengeState{}
	}
	if s.Authors == nil {
		s.Authors = map[string]*authorState{}
	}
	return s, nil
}
func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".tpctf-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), path)
}
func (s *state) save(dir string) error { return saveJSON(filepath.Join(dir, "state.json"), s) }
func (s *state) challenge(id string) *challengeState {
	v := s.Challenges[id]
	if v == nil {
		v = &challengeState{Submitted: map[string]string{}}
		s.Challenges[id] = v
	}
	if v.Submitted == nil {
		v.Submitted = map[string]string{}
	}
	return v
}
func digest(v any) string            { data, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(data)) }
func taskDir(root, id string) string { return filepath.Join(root, "challenges", digest(id)[:16]) }
