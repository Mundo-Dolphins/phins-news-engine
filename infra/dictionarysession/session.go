package dictionarysession

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mundo-Dolphins/phins-news-engine/app/dictionaryupdate"
)

type Status string

const (
	StatusOpen     Status = "open"
	StatusComplete Status = "complete"
	StatusAborted  Status = "aborted"
)

type DecisionAction string

const (
	DecisionAccept DecisionAction = "accept"
	DecisionReject DecisionAction = "reject"
	DecisionSkip   DecisionAction = "skip"
)

type Decision struct {
	ProposalIndex int
	Action        DecisionAction
	Edited        *dictionaryupdate.Proposal
	DecidedAt     time.Time
}

type Session struct {
	ID                 string
	SnapshotRefs       []string
	InitialVersion     string
	InitialContentHash string
	Proposals          []dictionaryupdate.Proposal
	Decisions          []Decision
	Cursor             int
	Status             Status
	CreatedAt          time.Time
	UpdatedAt          time.Time
	RulesetVersion     string
}

func New(id string, snapshotRefs []string, initialVersion, initialContentHash string, proposals []dictionaryupdate.Proposal, now time.Time) Session {
	return Session{
		ID: id, SnapshotRefs: snapshotRefs, InitialVersion: initialVersion, InitialContentHash: initialContentHash,
		Proposals: proposals, Status: StatusOpen, CreatedAt: now, UpdatedAt: now, RulesetVersion: "1",
	}
}

func sessionPath(baseDir, id string) string {
	return filepath.Join(baseDir, id+".json")
}

// Save writes the session atomically (temp file + rename), matching
// dictionaryfile.Save's convention — every accepted/rejected decision is
// saved immediately by the caller, so a crash loses at most the proposal
// currently in flight.
func Save(baseDir string, s Session) error {
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return fmt.Errorf("dictionarysession: mkdir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("dictionarysession: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(baseDir, ".session-*.tmp")
	if err != nil {
		return fmt.Errorf("dictionarysession: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("dictionarysession: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("dictionarysession: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, sessionPath(baseDir, s.ID)); err != nil {
		return fmt.Errorf("dictionarysession: rename temp file: %w", err)
	}
	return nil
}

func Load(baseDir, id string) (Session, error) {
	data, err := os.ReadFile(sessionPath(baseDir, id))
	if err != nil {
		return Session{}, fmt.Errorf("dictionarysession: read: %w", err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return Session{}, fmt.Errorf("dictionarysession: unmarshal: %w", err)
	}
	return s, nil
}

// LatestOpen returns the most recently updated session with Status ==
// StatusOpen, or an error if none exists.
func LatestOpen(baseDir string) (Session, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return Session{}, fmt.Errorf("dictionarysession: read dir: %w", err)
	}
	var best Session
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		s, err := Load(baseDir, id)
		if err != nil || s.Status != StatusOpen {
			continue
		}
		if !found || s.UpdatedAt.After(best.UpdatedAt) {
			best = s
			found = true
		}
	}
	if !found {
		return Session{}, fmt.Errorf("dictionarysession: no open session found in %s", baseDir)
	}
	return best, nil
}

func Discard(baseDir, id string) error {
	if err := os.Remove(sessionPath(baseDir, id)); err != nil {
		return fmt.Errorf("dictionarysession: discard: %w", err)
	}
	return nil
}
