package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const SessionLifecycleVersion = 1

type AttachmentKind string

const (
	AttachmentWorkspace     AttachmentKind = "workspace"
	AttachmentNamedSession  AttachmentKind = "named-session"
	AttachmentLegacyProfile AttachmentKind = "legacy-profile"
)

type AttachmentPhase string

const (
	AttachmentCreating AttachmentPhase = "creating"
	AttachmentActive   AttachmentPhase = "active"
	AttachmentClosing  AttachmentPhase = "closing"
	AttachmentClosed   AttachmentPhase = "closed"
)

type RetirementOperation string

const (
	RetirementMerge  RetirementOperation = "merge"
	RetirementDelete RetirementOperation = "delete"
)

type RetirementPhase string

const (
	RetirementChecking   RetirementPhase = "checking"
	RetirementPaused     RetirementPhase = "paused"
	RetirementClosing    RetirementPhase = "closing"
	RetirementFinalCheck RetirementPhase = "final-check"
	RetirementRemoving   RetirementPhase = "removing"
)

type EffectStatus string

const (
	EffectIntended EffectStatus = "intended"
	EffectDone     EffectStatus = "done"
)

const (
	EffectRemoteCheckout = "remote-checkout"
	EffectLocalWorktree  = "local-worktree"
	EffectBeadsRemote    = "beads-remote"
	EffectGitRemote      = "git-remote"
	EffectGitBranch      = "git-branch"
	EffectReplayJournal  = "replay-journal"
)

// HerdrAddress scopes a resource handle to the server that issued it.
// Session is always explicit, including the default session.
type HerdrAddress struct {
	MachineID  string `json:"machine_id,omitempty"`
	Session    string `json:"session"`
	ConfigRoot string `json:"config_root"`
}

// OwnershipProof is interpreted only by the adapter named by Scheme.
type OwnershipProof struct {
	Scheme string          `json:"scheme"`
	Data   json.RawMessage `json:"data"`
}

type HerdrAttachment struct {
	ID      string          `json:"id"`
	Kind    AttachmentKind  `json:"kind"`
	Address HerdrAddress    `json:"address"`
	Handle  string          `json:"handle,omitempty"`
	Proof   OwnershipProof  `json:"proof,omitempty"`
	Owned   bool            `json:"owned"`
	Phase   AttachmentPhase `json:"phase"`
}

type Destination struct {
	LocalRepo string `json:"local_repo"`
	GitDir    string `json:"git_dir"`
	Branch    string `json:"branch"`
}

type Acceptance struct {
	SourceTip     string `json:"source_tip"`
	ReplayBase    string `json:"replay_base"`
	VerifiedHead  string `json:"verified_head"`
	IssueEvidence string `json:"issue_evidence,omitempty"`
	// Issues is what the merge's issue check found, kept with the evidence
	// so the command that finishes the retirement can print it even when an
	// earlier run did the checking.
	Issues *IssueReceipt `json:"issues,omitempty"`
}

// IssueReceipt summarises a session's issue changes and which of them have
// not reached the local database. Accepted marks a merge that went ahead
// past those with --accept-unsynced.
type IssueReceipt struct {
	Created  int             `json:"created"`
	Closed   int             `json:"closed"`
	Updated  int             `json:"updated"`
	Removed  int             `json:"removed"`
	Unsynced []UnsyncedIssue `json:"unsynced,omitempty"`
	Accepted bool            `json:"accepted,omitempty"`
}

// UnsyncedIssue is one change the local database does not hold. Times are
// RFC 3339 strings so the record stays a plain value; LocalUpdated is empty
// when the issue is missing locally or the local database was unreadable,
// and Sync says which.
type UnsyncedIssue struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Sync            string `json:"sync"`
	InstanceUpdated string `json:"instance_updated,omitempty"`
	LocalUpdated    string `json:"local_updated,omitempty"`
}

// PausedOnIssues is the Retirement.PausedOn reason for a merge waiting on
// issues that have not landed. A reason rather than a phase: a new phase
// would have to be taught to the validator, both resume checks and both
// attach guards, and an older binary would reject the state file.
const PausedOnIssues = "issues"

type RetirementEffect struct {
	Status EffectStatus `json:"status"`
}

type Retirement struct {
	Version     int                         `json:"version"`
	Operation   RetirementOperation         `json:"operation"`
	Force       bool                        `json:"force,omitempty"`
	Phase       RetirementPhase             `json:"phase"`
	Destination Destination                 `json:"destination"`
	Accepted    []Acceptance                `json:"accepted,omitempty"`
	Signed      []string                    `json:"signed,omitempty"`
	Effects     map[string]RetirementEffect `json:"effects"`
	LastFailure string                      `json:"last_failure,omitempty"`
	// PausedOn names why a paused retirement stopped when the reason is one
	// the user acts on differently (see PausedOnIssues); empty otherwise.
	PausedOn string `json:"paused_on,omitempty"`
}

type RetirementReceipt struct {
	Session     string              `json:"session"`
	Generation  string              `json:"generation"`
	LocalRepo   string              `json:"local_repo"`
	Operation   RetirementOperation `json:"operation"`
	Signed      []string            `json:"signed,omitempty"`
	CompletedAt time.Time           `json:"completed_at"`
}

// NewSessionGeneration returns an opaque 128-bit identity. A generation is
// assigned once, before any external start effect, and is never derived from
// a reusable session name.
func NewSessionGeneration() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create session generation: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// PrepareSessionStart creates a generation or updates only the retryable
// identity fields of the existing generation. Ownership and retirement state
// can therefore never be reset by reconstructing a Session literal.
func (r *Record) PrepareSessionStart(name, localRepo, base, repoName string) (Session, error) {
	if existing, ok := r.FindSession(name); ok {
		if existing.Retirement != nil {
			return Session{}, fmt.Errorf("session %s has %s retirement pending; resume that operation instead", name, existing.Retirement.Operation)
		}
		if existing.LocalRepo != "" && existing.LocalRepo != localRepo || existing.Base != "" && existing.Base != base || existing.RepoName != "" && existing.RepoName != repoName {
			return Session{}, fmt.Errorf("session %s already belongs to a different checkout or base", name)
		}
		if existing.Generation == "" {
			generation, err := NewSessionGeneration()
			if err != nil {
				return Session{}, err
			}
			existing.Generation = generation
		}
		existing.LocalRepo = localRepo
		existing.Base = base
		existing.RepoName = repoName
		r.PutSession(existing)
		return existing, nil
	}

	generation, err := NewSessionGeneration()
	if err != nil {
		return Session{}, err
	}
	sess := Session{
		Name: name, LocalRepo: localRepo, Base: base, RepoName: repoName,
		Generation: generation, AttachmentsKnown: true,
	}
	r.PutSession(sess)

	// A completed generation must not make a newly active use of the same
	// name look already retired. Receipts for other repositories survive.
	kept := make([]RetirementReceipt, 0, len(r.RetirementReceipts))
	for _, receipt := range r.RetirementReceipts {
		if receipt.Session != name || receipt.LocalRepo != localRepo {
			kept = append(kept, receipt)
		}
	}
	r.RetirementReceipts = kept
	return sess, nil
}

func (r Record) FindRetirementReceipt(session, localRepo string) (RetirementReceipt, bool) {
	for _, receipt := range r.RetirementReceipts {
		if receipt.Session == session && receipt.LocalRepo == localRepo {
			return receipt, true
		}
	}
	return RetirementReceipt{}, false
}

// ValidateSessionLifecycle rejects malformed required state while retaining
// the all-zero lifecycle fields of records written by older versions.
func ValidateSessionLifecycle(sess Session) error {
	if sess.Generation != "" {
		if err := validateGeneration(sess.Generation); err != nil {
			return err
		}
	}
	if !sess.AttachmentsKnown && len(sess.Attachments) != 0 {
		return fmt.Errorf("session %s has attachments while ownership is unknown", sess.Name)
	}
	seen := make(map[string]struct{}, len(sess.Attachments))
	for _, attachment := range sess.Attachments {
		if err := validateAttachment(attachment); err != nil {
			return fmt.Errorf("session %s attachment: %w", sess.Name, err)
		}
		if _, exists := seen[attachment.ID]; exists {
			return fmt.Errorf("session %s has duplicate attachment id %q", sess.Name, attachment.ID)
		}
		seen[attachment.ID] = struct{}{}
	}
	if sess.Retirement == nil {
		return nil
	}
	if sess.Generation == "" {
		return fmt.Errorf("session %s retirement has no generation", sess.Name)
	}
	return validateRetirement(sess.Name, *sess.Retirement)
}

func validateGeneration(generation string) error {
	raw, err := hex.DecodeString(generation)
	if err != nil || len(raw) != 16 {
		return fmt.Errorf("malformed session generation %q", generation)
	}
	return nil
}

func validateAttachment(attachment HerdrAttachment) error {
	if attachment.ID == "" {
		return fmt.Errorf("missing id")
	}
	switch attachment.Kind {
	case AttachmentWorkspace:
		if attachment.Address.MachineID == "" || attachment.Address.Session == "" || attachment.Address.ConfigRoot == "" {
			return fmt.Errorf("workspace %s has an incomplete server address", attachment.ID)
		}
	case AttachmentNamedSession:
		if attachment.Address.Session == "" || attachment.Address.ConfigRoot == "" {
			return fmt.Errorf("named session %s has an incomplete server address", attachment.ID)
		}
	case AttachmentLegacyProfile:
	default:
		return fmt.Errorf("%s has unknown kind %q", attachment.ID, attachment.Kind)
	}
	switch attachment.Phase {
	case AttachmentCreating:
	case AttachmentActive, AttachmentClosing, AttachmentClosed:
		if attachment.Handle == "" {
			return fmt.Errorf("%s in phase %s has no handle", attachment.ID, attachment.Phase)
		}
	default:
		return fmt.Errorf("%s has unknown phase %q", attachment.ID, attachment.Phase)
	}
	if attachment.Owned && attachment.Phase != AttachmentCreating {
		if attachment.Proof.Scheme == "" || len(attachment.Proof.Data) == 0 || !json.Valid(attachment.Proof.Data) {
			return fmt.Errorf("owned attachment %s has malformed ownership proof", attachment.ID)
		}
	}
	return nil
}

func validateRetirement(session string, retirement Retirement) error {
	if retirement.Version != SessionLifecycleVersion {
		return fmt.Errorf("session %s has unsupported retirement version %d", session, retirement.Version)
	}
	if retirement.Operation != RetirementMerge && retirement.Operation != RetirementDelete {
		return fmt.Errorf("session %s has unknown retirement operation %q", session, retirement.Operation)
	}
	if retirement.Force && retirement.Operation != RetirementDelete {
		return fmt.Errorf("session %s cannot force a %s retirement", session, retirement.Operation)
	}
	switch retirement.Phase {
	case RetirementChecking, RetirementPaused, RetirementClosing, RetirementFinalCheck, RetirementRemoving:
	default:
		return fmt.Errorf("session %s has unknown retirement phase %q", session, retirement.Phase)
	}
	if retirement.Destination.LocalRepo == "" || retirement.Destination.GitDir == "" || retirement.Destination.Branch == "" {
		return fmt.Errorf("session %s retirement has an incomplete destination", session)
	}
	if retirement.Effects == nil {
		return fmt.Errorf("session %s retirement has no effect journal", session)
	}
	for i, accepted := range retirement.Accepted {
		if accepted.SourceTip == "" || accepted.ReplayBase == "" || accepted.VerifiedHead == "" {
			return fmt.Errorf("session %s acceptance %d is incomplete", session, i)
		}
	}
	advanced := retirement.Phase == RetirementClosing || retirement.Phase == RetirementFinalCheck || retirement.Phase == RetirementRemoving
	if advanced && len(retirement.Accepted) == 0 && (retirement.Operation != RetirementDelete || !retirement.Force) {
		return fmt.Errorf("session %s cannot enter %s without accepted safety evidence", session, retirement.Phase)
	}
	for key, effect := range retirement.Effects {
		if !validEffectKey(key) {
			return fmt.Errorf("session %s has unknown retirement effect %q", session, key)
		}
		if effect.Status != EffectIntended && effect.Status != EffectDone {
			return fmt.Errorf("session %s effect %s has unknown status %q", session, key, effect.Status)
		}
	}
	return nil
}

func validEffectKey(key string) bool {
	if strings.HasPrefix(key, "herdr:") {
		return len(strings.TrimPrefix(key, "herdr:")) != 0
	}
	switch key {
	case EffectRemoteCheckout, EffectLocalWorktree, EffectBeadsRemote, EffectGitRemote, EffectGitBranch, EffectReplayJournal:
		return true
	default:
		return false
	}
}

func validateRecordLifecycle(record Record) error {
	for _, sess := range record.Sessions {
		if err := ValidateSessionLifecycle(sess); err != nil {
			return err
		}
	}
	for _, receipt := range record.RetirementReceipts {
		if receipt.Session == "" || receipt.LocalRepo == "" || receipt.CompletedAt.IsZero() {
			return fmt.Errorf("instance %s has an incomplete retirement receipt", record.Name)
		}
		if err := validateGeneration(receipt.Generation); err != nil {
			return fmt.Errorf("instance %s receipt: %w", record.Name, err)
		}
		if receipt.Operation != RetirementMerge && receipt.Operation != RetirementDelete {
			return fmt.Errorf("instance %s receipt has unknown operation %q", record.Name, receipt.Operation)
		}
	}
	return nil
}
