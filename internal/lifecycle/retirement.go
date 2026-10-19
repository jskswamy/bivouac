package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jskswamy/bivouac/internal/state"
)

type RetirementOptions struct {
	Operation state.RetirementOperation
	Force     bool
}

type RetirementResult struct {
	Completed        bool
	AlreadyCompleted bool
	Phase            state.RetirementPhase
	Signed           []string
}

type RetirementError struct {
	Operation     state.RetirementOperation
	Phase         state.RetirementPhase
	ResumeCommand string
	Err           error
}

func (e *RetirementError) Error() string {
	return fmt.Sprintf("%s retirement is %s: %v\nresume with: %s", e.Operation, e.Phase, e.Err, e.ResumeCommand)
}

func (e *RetirementError) Unwrap() error { return e.Err }

var (
	closeRetirementAttachment = CloseSessionAttachment
	checkRetirementWork       = func(policy retirementPolicy, ctx context.Context, record state.Record, sess state.Session, final bool) (state.Acceptance, []string, error) {
		return policy.Check(ctx, record, sess, final)
	}
	validateRetirementWork = func(policy retirementPolicy, ctx context.Context, record state.Record, sess state.Session, accepted state.Acceptance) error {
		return policy.Validate(ctx, record, sess, accepted)
	}
	runRetirementEffectsFn = runRetirementEffects
	retirementNow          = time.Now
)

func RetireSession(ctx context.Context, store *state.Store, instance, session string, options RetirementOptions) (RetirementResult, error) {
	if options.Operation != state.RetirementMerge && options.Operation != state.RetirementDelete {
		return RetirementResult{}, fmt.Errorf("unsupported retirement operation %q", options.Operation)
	}
	if options.Force && options.Operation != state.RetirementDelete {
		return RetirementResult{}, fmt.Errorf("force is valid only for delete")
	}
	instanceRelease, err := store.LockInstance(ctx, instance, false)
	if err != nil {
		return RetirementResult{}, err
	}
	defer func() { _ = instanceRelease() }()
	sessionRelease, err := store.LockSession(ctx, instance, session)
	if err != nil {
		return RetirementResult{}, err
	}
	defer func() { _ = sessionRelease() }()

	record, ok, err := store.Get(instance)
	if err != nil {
		return RetirementResult{}, err
	}
	if !ok {
		return RetirementResult{}, fmt.Errorf("instance %s not found", instance)
	}
	sess, active := record.FindSession(session)
	if !active {
		for _, receipt := range record.RetirementReceipts {
			if receipt.Session == session && receipt.Operation == options.Operation {
				return RetirementResult{Completed: true, AlreadyCompleted: true, Signed: receipt.Signed}, nil
			}
		}
		return RetirementResult{}, fmt.Errorf("instance %s has no session %q", instance, session)
	}
	if !sess.AttachmentsKnown {
		return RetirementResult{}, retirementFailure(options.Operation, sess.Name, state.RetirementPaused,
			fmt.Errorf("session attachment ownership is unknown; run `bivouac attach %s` interactively to confirm legacy Herdr ownership before retirement", sess.Name))
	}
	if sess.Generation == "" {
		generation, err := state.NewSessionGeneration()
		if err != nil {
			return RetirementResult{}, err
		}
		if err := store.Update(instance, func(latest *state.Record) error {
			current, ok := latest.FindSession(session)
			if !ok {
				return fmt.Errorf("session %s disappeared", session)
			}
			if current.Generation == "" {
				current.Generation = generation
				latest.PutSession(current)
			}
			return nil
		}); err != nil {
			return RetirementResult{}, err
		}
		record, _, _ = store.Get(instance)
		sess, _ = record.FindSession(session)
	}

	// An intent exists so a resumed retirement keeps its policy once work has
	// been accepted or an effect started. One that is still checking, or was
	// paused before accepting anything, has protected nothing yet -- a merge
	// refused on its precondition is the case -- so a different operation may
	// replace it rather than leave the session retirable only by tearing down
	// the instance.
	if r := sess.Retirement; r != nil && (r.Operation != options.Operation || r.Force != options.Force) &&
		(r.Phase == state.RetirementChecking || r.Phase == state.RetirementPaused) &&
		len(r.Accepted) == 0 && len(r.Effects) == 0 {
		sess.Retirement = nil
	}

	// Raising force on the same delete is the user adding authority, not
	// taking it away, so it is allowed until the cleanup effects begin. The
	// intent is immutable so an interrupted *forced* delete cannot resume
	// without its authority; nothing is lost by upgrading a plain one, whose
	// accepted work was checked under the stricter policy. A plain delete that
	// passed its work check and then paused on a cleanup step is the case: it
	// has accepted work, so the replacement above cannot apply to it.
	if r := sess.Retirement; r != nil && r.Operation == state.RetirementDelete && options.Operation == state.RetirementDelete &&
		!r.Force && options.Force && r.Phase != state.RetirementRemoving {
		if err := updateRetirement(store, instance, sess.Generation, func(current *state.Session) error {
			current.Retirement.Force = true
			return nil
		}); err != nil {
			return RetirementResult{}, err
		}
		r.Force = true
	}

	if sess.Retirement == nil {
		destination, err := retirementDestinationState(ctx, sess)
		if err != nil {
			return RetirementResult{}, err
		}
		retirement := &state.Retirement{
			Version: state.SessionLifecycleVersion, Operation: options.Operation,
			Force: options.Force, Phase: state.RetirementChecking,
			Destination: destination, Effects: map[string]state.RetirementEffect{},
		}
		if err := updateRetirement(store, instance, sess.Generation, func(current *state.Session) error {
			current.Retirement = retirement
			return nil
		}); err != nil {
			return RetirementResult{}, err
		}
		sess.Retirement = retirement
	} else if sess.Retirement.Operation != options.Operation || sess.Retirement.Force != options.Force {
		return RetirementResult{Phase: sess.Retirement.Phase}, retirementFailure(options.Operation, sess.Name, sess.Retirement.Phase,
			fmt.Errorf("session already has immutable %s retirement intent (force=%t)", sess.Retirement.Operation, sess.Retirement.Force))
	}

	policy := mergeRetirementPolicy()
	if options.Operation == state.RetirementDelete {
		policy = deleteRetirementPolicy(sess.Retirement.Force)
	}
	result := RetirementResult{Phase: sess.Retirement.Phase, Signed: append([]string(nil), sess.Retirement.Signed...)}
	closed := retirementAttachmentsDone(sess)
	if sess.Retirement.Phase == state.RetirementChecking || sess.Retirement.Phase == state.RetirementPaused {
		accepted, signed, err := checkRetirementWork(policy, ctx, record, sess, closed)
		if err != nil {
			return result, pauseRetirement(store, instance, sess, err)
		}
		if err := updateRetirement(store, instance, sess.Generation, func(current *state.Session) error {
			appendAcceptance(current.Retirement, accepted)
			current.Retirement.Signed = appendUnique(current.Retirement.Signed, signed...)
			if closed {
				current.Retirement.Phase = state.RetirementRemoving
			} else {
				current.Retirement.Phase = state.RetirementClosing
			}
			current.Retirement.LastFailure = ""
			return nil
		}); err != nil {
			return result, err
		}
		record, sess, err = reloadRetirement(store, instance, session)
		if err != nil {
			return result, err
		}
		result.Signed = append([]string(nil), sess.Retirement.Signed...)
	}

	if sess.Retirement.Phase == state.RetirementClosing {
		for _, attachment := range sess.Attachments {
			if attachment.Phase == state.AttachmentClosed {
				continue
			}
			key := "herdr:" + attachment.ID
			if err := intendEffect(store, instance, sess.Generation, key); err != nil {
				return result, err
			}
			if err := closeRetirementAttachment(record.ConnectionContext(ctx), record, attachment); err != nil {
				return result, pauseRetirement(store, instance, sess, err)
			}
			if err := completeAttachmentEffect(store, instance, sess.Generation, attachment.ID, key); err != nil {
				return result, err
			}
		}
		if err := setRetirementPhase(store, instance, sess.Generation, state.RetirementFinalCheck); err != nil {
			return result, err
		}
		record, sess, err = reloadRetirement(store, instance, session)
		if err != nil {
			return result, err
		}
	}

	if sess.Retirement.Phase == state.RetirementFinalCheck {
		accepted, signed, err := checkRetirementWork(policy, ctx, record, sess, true)
		if err != nil {
			return result, pauseRetirement(store, instance, sess, err)
		}
		if err := updateRetirement(store, instance, sess.Generation, func(current *state.Session) error {
			appendAcceptance(current.Retirement, accepted)
			current.Retirement.Signed = appendUnique(current.Retirement.Signed, signed...)
			current.Retirement.Phase = state.RetirementRemoving
			return nil
		}); err != nil {
			return result, err
		}
		record, sess, err = reloadRetirement(store, instance, session)
		if err != nil {
			return result, err
		}
	}

	if sess.Retirement.Phase == state.RetirementRemoving {
		if len(sess.Retirement.Accepted) == 0 {
			return result, pauseRetirement(store, instance, sess, errors.New("missing required acceptance evidence"))
		}
		accepted := sess.Retirement.Accepted[len(sess.Retirement.Accepted)-1]
		if err := validateRetirementWork(policy, ctx, record, sess, accepted); err != nil {
			return result, pauseRetirement(store, instance, sess, err)
		}
		if err := runRetirementEffectsFn(ctx, store, record, sess); err != nil {
			return result, pauseRetirement(store, instance, sess, err)
		}
		_, sess, err = reloadRetirement(store, instance, session)
		if err != nil {
			return result, err
		}
		if !retirementEffectsDone(sess) {
			return result, pauseRetirement(store, instance, sess, errors.New("retirement effects remain incomplete"))
		}
		receipt := state.RetirementReceipt{
			Session: sess.Name, Generation: sess.Generation, LocalRepo: sess.LocalRepo,
			Operation: sess.Retirement.Operation, Signed: sess.Retirement.Signed, CompletedAt: retirementNow(),
		}
		if err := store.CompleteRetirement(instance, sess.Generation, receipt); err != nil {
			return result, err
		}
		return RetirementResult{Completed: true, Phase: state.RetirementRemoving, Signed: receipt.Signed}, nil
	}
	return result, retirementFailure(options.Operation, session, sess.Retirement.Phase, errors.New("retirement paused"))
}

func retirementDestinationState(ctx context.Context, sess state.Session) (state.Destination, error) {
	branchOut, err := runLocalGit(ctx, sess.LocalRepo, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return state.Destination{}, fmt.Errorf("reading retirement destination branch: %w\n%s", err, branchOut)
	}
	branch := trimLine(branchOut)
	gitDir, err := runLocalGit(ctx, sess.LocalRepo, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return state.Destination{}, fmt.Errorf("locating retirement git directory: %w\n%s", err, gitDir)
	}
	return state.Destination{LocalRepo: sess.LocalRepo, GitDir: trimLine(gitDir), Branch: branch}, nil
}

func reloadRetirement(store *state.Store, instance, session string) (state.Record, state.Session, error) {
	record, ok, err := store.Get(instance)
	if err != nil || !ok {
		return state.Record{}, state.Session{}, errors.Join(err, fmt.Errorf("instance %s disappeared", instance))
	}
	sess, ok := record.FindSession(session)
	if !ok || sess.Retirement == nil {
		return state.Record{}, state.Session{}, fmt.Errorf("session %s retirement disappeared", session)
	}
	return record, sess, nil
}

func updateRetirement(store *state.Store, instance, generation string, mutate func(*state.Session) error) error {
	return store.Update(instance, func(record *state.Record) error {
		for i := range record.Sessions {
			if record.Sessions[i].Generation == generation {
				return mutate(&record.Sessions[i])
			}
		}
		return fmt.Errorf("session generation %s is no longer active", generation)
	})
}

func appendAcceptance(retirement *state.Retirement, accepted state.Acceptance) {
	for i := range retirement.Accepted {
		if retirement.Accepted[i].SourceTip == accepted.SourceTip {
			retirement.Accepted[i] = accepted
			return
		}
	}
	retirement.Accepted = append(retirement.Accepted, accepted)
}

func appendUnique(existing []string, values ...string) []string {
	seen := make(map[string]bool, len(existing)+len(values))
	for _, value := range existing {
		seen[value] = true
	}
	for _, value := range values {
		if !seen[value] {
			existing = append(existing, value)
			seen[value] = true
		}
	}
	return existing
}

func retirementAttachmentsDone(sess state.Session) bool {
	if sess.Retirement == nil || len(sess.Retirement.Accepted) == 0 {
		return false
	}
	for _, attachment := range sess.Attachments {
		if attachment.Phase != state.AttachmentClosed {
			return false
		}
	}
	return true
}

func retirementEffectsDone(sess state.Session) bool {
	for _, attachment := range sess.Attachments {
		if sess.Retirement.Effects["herdr:"+attachment.ID].Status != state.EffectDone {
			return false
		}
	}
	for _, key := range retirementEffectOrder {
		if sess.Retirement.Effects[key].Status != state.EffectDone {
			return false
		}
	}
	return true
}

func intendEffect(store *state.Store, instance, generation, key string) error {
	return updateRetirement(store, instance, generation, func(sess *state.Session) error {
		if sess.Retirement.Effects[key].Status != state.EffectDone {
			sess.Retirement.Effects[key] = state.RetirementEffect{Status: state.EffectIntended}
		}
		return nil
	})
}

func completeAttachmentEffect(store *state.Store, instance, generation, attachmentID, key string) error {
	return updateRetirement(store, instance, generation, func(sess *state.Session) error {
		for i := range sess.Attachments {
			if sess.Attachments[i].ID == attachmentID {
				sess.Attachments[i].Phase = state.AttachmentClosed
			}
		}
		sess.Retirement.Effects[key] = state.RetirementEffect{Status: state.EffectDone}
		return nil
	})
}

func setRetirementPhase(store *state.Store, instance, generation string, phase state.RetirementPhase) error {
	return updateRetirement(store, instance, generation, func(sess *state.Session) error {
		sess.Retirement.Phase = phase
		return nil
	})
}

func pauseRetirement(store *state.Store, instance string, sess state.Session, cause error) error {
	phase := state.RetirementPaused
	if sess.Retirement != nil && sess.Retirement.Phase == state.RetirementRemoving {
		phase = state.RetirementRemoving
	}
	recordErr := updateRetirement(store, instance, sess.Generation, func(current *state.Session) error {
		current.Retirement.Phase = phase
		current.Retirement.LastFailure = cause.Error()
		return nil
	})
	if recordErr != nil {
		cause = errors.Join(cause, fmt.Errorf("recording retirement failure: %w", recordErr))
	}
	return retirementFailure(sess.Retirement.Operation, sess.Name, phase, cause)
}

func retirementFailure(operation state.RetirementOperation, session string, phase state.RetirementPhase, cause error) error {
	command := "bivouac session " + string(operation) + " " + session
	return &RetirementError{Operation: operation, Phase: phase, ResumeCommand: command, Err: cause}
}
