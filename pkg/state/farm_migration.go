package state

import "time"

// FarmMigration records the one-time move of the plugin farm to the plugin
// manager. It is the state that makes the move resumable: every stage writes
// what it did and where it stopped, so a process killed mid-migration re-runs
// from the recorded stage instead of starting over or, worse, continuing past
// a half-finished deletion.
//
// The backup path is part of the record, not a log line: it is how a user gets
// back, and it must survive the migration that removes the farm.
type FarmMigration struct {
	// Done is set by the final stage. A vault with Done set is never migrated
	// again, whatever the ledger says.
	Done bool `json:"done,omitempty"`
	// Stage is the last stage that completed. An empty Stage means the
	// migration has not started.
	Stage string `json:"stage,omitempty"`
	// Backup is the timestamped directory taken before anything was removed.
	Backup string `json:"backup,omitempty"`
	// Adopted lists the plugin keys the manager has taken over, so a resumed
	// run does not adopt one twice.
	Adopted []string `json:"adopted,omitempty"`
	// Skipped names the plugin keys the library could not resolve, and why, in
	// the library's own words. A plugin that left its marketplace or lives in a
	// private repository is not a reason to stall the whole migration: the rest
	// moves, this one waits for the user, and the record is what doctor reads to
	// name it.
	Skipped map[string]string `json:"skipped,omitempty"`
	// SkippedAt is when each refusal was recorded, so a report can say how long
	// a plugin has been waiting rather than only that it is.
	SkippedAt map[string]time.Time `json:"skipped_at,omitzero"`
	// Removed lists the exact paths the migration deleted. Nothing outside
	// this list is ever removed, and it is what the reversal reads.
	Removed []string `json:"removed,omitempty"`
	// Reapprovals lists the hook names whose approval was recorded in beadle
	// and must be given again in the plugin manager. They are surfaced, never
	// carried silently: the two approval stores are keyed differently.
	Reapprovals []string  `json:"reapprovals,omitempty"`
	StartedAt   time.Time `json:"started_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
}

// FarmMigration stages, in order. Each one is idempotent: re-running it either
// does nothing or redoes the same thing, and each one advances Stage only
// after its own work is complete.
const (
	// FarmStageBackup is the copy of the state the reversal needs. Nothing is
	// removed before it completes.
	FarmStageBackup = "backup"
	// FarmStageHome creates the plugin manager's home beside the vault.
	FarmStageHome = "home"
	// FarmStageImport carries beadle's pins and approvals across.
	FarmStageImport = "import"
	// FarmStageAdopt hands every farmed plugin to the manager.
	FarmStageAdopt = "adopt"
	// FarmStageClaim checks the manager answers for every farm path before
	// anything is written over one.
	FarmStageClaim = "claim"
	// FarmStageDeliver replaces the farm's symlinks with delivered files.
	FarmStageDeliver = "deliver"
	// FarmStageRemove deletes the farm — recorded paths only.
	FarmStageRemove = "remove"
	// FarmStageVerify is the last stage: the status matrix and a second dry run
	// that must come back empty.
	FarmStageVerify = "verify"
)

// FarmStageOrder is the order the stages run in. A resumed run starts at the
// first stage it has not recorded.
var FarmStageOrder = []string{
	FarmStageBackup,
	FarmStageHome,
	FarmStageImport,
	FarmStageAdopt,
	FarmStageClaim,
	FarmStageDeliver,
	FarmStageRemove,
	FarmStageVerify,
}

// NeedsFarmMigration reports whether this vault still has to be migrated. A
// vault that has never recorded a migration and has a farm does; a vault whose
// migration is done never does, whatever happens to the ledger afterwards.
func (s *State) NeedsFarmMigration(hasFarm bool) bool {
	if s == nil || !hasFarm {
		return false
	}

	return s.FarmMigration == nil || !s.FarmMigration.Done
}

// EnsureFarmMigration returns the record, creating it on first use. The
// migration writes to it stage by stage, so a nil record is a normal state and
// not an error.
func (s *State) EnsureFarmMigration() *FarmMigration {
	if s.FarmMigration == nil {
		s.FarmMigration = &FarmMigration{}
	}

	return s.FarmMigration
}

// FarmMigrationStage is the stage this vault has completed, or the empty string
// when it has never started.
func (s *State) FarmMigrationStage() string {
	if s == nil || s.FarmMigration == nil {
		return ""
	}

	return s.FarmMigration.Stage
}

// FarmStageIndex is the position of stage in the order, and -1 for a stage the
// order does not know — which happens only when a future version adds one.
func FarmStageIndex(stage string) int {
	for i, candidate := range FarmStageOrder {
		if candidate == stage {
			return i
		}
	}

	return -1
}

// FarmStageNext is the first stage after the recorded one. An empty or unknown
// stage starts at the first stage in the order.
func FarmStageNext(stage string) string {
	next := FarmStageIndex(stage) + 1
	if next < 0 || next >= len(FarmStageOrder) {
		if stage == "" {
			return FarmStageOrder[0]
		}

		return ""
	}

	return FarmStageOrder[next]
}

// RecordFarmStage advances the record and returns the next stage to run. The
// caller persists the state after every stage, which is what makes a crash
// resumable: the worst case is a stage that ran twice, and every stage is
// written so that running it twice is the same as running it once.
func (s *State) RecordFarmStage(stage string, now time.Time) string {
	if s.FarmMigration == nil {
		s.FarmMigration = &FarmMigration{StartedAt: now}
	}

	s.FarmMigration.Stage = stage
	s.FarmMigration.UpdatedAt = now

	if stage == FarmStageVerify {
		s.FarmMigration.Done = true
	}

	return FarmStageNext(stage)
}

// FarmStagePending reports whether stage still has to run.
func (s *State) FarmStagePending(stage string) bool {
	if s == nil {
		return false
	}

	recorded := ""
	if s.FarmMigration != nil {
		recorded = s.FarmMigration.Stage
	}

	done := FarmStageIndex(stage)
	have := FarmStageIndex(recorded)

	return done > have
}
