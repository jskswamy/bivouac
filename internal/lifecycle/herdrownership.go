package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jskswamy/bivouac/internal/reconcile"
	"github.com/jskswamy/bivouac/internal/shellcmd"
	"github.com/jskswamy/bivouac/internal/state"
	"github.com/jskswamy/bivouac/internal/tool"
)

const (
	herdrConfigRoot             = "~/.config/herdr"
	workspaceTerminalProof      = "herdr-workspace-root-terminal-v1"
	namedSessionIntentProof     = "bivouac-named-session-intent-v1"
	attachmentIntentLabelPrefix = "bivouac-attachment-"
)

type namedSessionEvidence struct {
	Session string `json:"session"`
	Token   string `json:"token"`
}

// errOwnershipUnproven marks a workspace that exists but that bivouac can no
// longer show is its own, as opposed to one it failed to close. Retirement
// treats the two differently under --force.
var errOwnershipUnproven = errors.New("ownership cannot be proved")

type workspaceTerminalEvidence struct {
	PaneID     string `json:"paneId"`
	TerminalID string `json:"terminalId"`
}

type workspaceCreateReply struct {
	Result struct {
		Workspace herdrWorkspace `json:"workspace"`
		RootPane  struct {
			ID         string `json:"pane_id"`
			TerminalID string `json:"terminal_id"`
		} `json:"root_pane"`
	} `json:"result"`
}

type ownershipPane struct {
	ID         string `json:"pane_id"`
	TerminalID string `json:"terminal_id"`
}

type closeableRemoteRunner interface {
	remoteRunner
	Close() error
}

var (
	attachmentHerdrRunner   = func(ctx context.Context) herdrRunner { return localHerdr{ctx: ctx} }
	attachmentRemoteConnect = func(ctx context.Context, ip, user string) (closeableRemoteRunner, error) {
		return reconcile.Connect(ctx, ip, user)
	}
	requireHerdrTool = tool.Require
)

// EnsureSessionAttachment creates or recovers the owned workspace for one
// active session. Creation intent is durable before Herdr is mutated, and the
// returned handle is never authorised by its friendly label.
func EnsureSessionAttachment(ctx context.Context, store *state.Store, instance, session string) (state.HerdrAttachment, error) {
	instanceRelease, err := store.LockInstance(ctx, instance, false)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	defer func() { _ = instanceRelease() }()
	sessionRelease, err := store.LockSession(ctx, instance, session)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	defer func() { _ = sessionRelease() }()

	record, ok, err := store.Get(instance)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	if !ok {
		return state.HerdrAttachment{}, fmt.Errorf("instance %s not found", instance)
	}
	sess, ok := record.FindSession(session)
	if !ok {
		return state.HerdrAttachment{}, fmt.Errorf("instance %s has no session %q", instance, session)
	}
	if !sess.AttachmentsKnown {
		return state.HerdrAttachment{}, fmt.Errorf("session %s predates attachment ownership tracking; inspect its exact Herdr resource and confirm adoption interactively before attaching", session)
	}
	if sess.Retirement != nil && sess.Retirement.Phase != state.RetirementPaused {
		return state.HerdrAttachment{}, fmt.Errorf("session %s retirement is %s; resume %s instead of attaching", session, sess.Retirement.Phase, sess.Retirement.Operation)
	}
	for _, attachment := range sess.Attachments {
		if attachment.Kind == state.AttachmentWorkspace && attachment.Phase != state.AttachmentClosed {
			return ensureWorkspaceAttachment(ctx, store, record, sess, attachment)
		}
	}

	if _, err := requireHerdrTool("herdr"); err != nil {
		return state.HerdrAttachment{}, err
	}
	ctx = record.ConnectionContext(ctx)
	if _, _, err := instanceTransport(ctx, record.IP, record.User); err != nil {
		return state.HerdrAttachment{}, err
	}
	target, err := MachineTarget(ctx, record.IP, record.User)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	runner := attachmentHerdrRunner(ctx)
	machineID, _, err := EnsureMachine(runner, record.Name, target, record.HerdrMachineID)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	id, err := state.NewSessionGeneration()
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	attachment := state.HerdrAttachment{
		ID: id, Kind: state.AttachmentWorkspace,
		Address: state.HerdrAddress{MachineID: machineID, Session: "default", ConfigRoot: herdrConfigRoot},
		Owned:   true, Phase: state.AttachmentCreating,
	}
	if err := store.Update(instance, func(latest *state.Record) error {
		latest.HerdrMachineID = machineID
		current, ok := latest.FindSession(session)
		if !ok || current.Generation != sess.Generation {
			return fmt.Errorf("session %s generation changed while preparing Herdr", session)
		}
		current.Attachments = append(current.Attachments, attachment)
		invalidateFinalRetirementEvidence(&current)
		latest.PutSession(current)
		return nil
	}); err != nil {
		return state.HerdrAttachment{}, err
	}
	record.HerdrMachineID = machineID
	return ensureWorkspaceAttachment(ctx, store, record, sess, attachment)
}

func ensureWorkspaceAttachment(ctx context.Context, store *state.Store, record state.Record, sess state.Session, attachment state.HerdrAttachment) (state.HerdrAttachment, error) {
	runner := attachmentHerdrRunner(ctx)
	if err := verifyMachineAddress(runner, record, attachment.Address); err != nil {
		return state.HerdrAttachment{}, err
	}
	if attachment.Phase == state.AttachmentActive {
		if err := verifyWorkspaceOwnership(runner, attachment); err != nil {
			return state.HerdrAttachment{}, err
		}
		if err := FocusWorkspace(runner, attachment.Address, attachment.Handle); err != nil {
			return state.HerdrAttachment{}, err
		}
		return attachment, nil
	}
	if attachment.Phase != state.AttachmentCreating {
		return state.HerdrAttachment{}, fmt.Errorf("attachment %s is %s and cannot be reopened", attachment.ID, attachment.Phase)
	}
	label := attachmentIntentLabel(attachment.ID)
	workspace, evidence, found, err := recoverIntendedWorkspace(runner, attachment.Address, label)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	if !found {
		args, err := herdrCommand(attachment.Address, "workspace", "create", "--cwd", RemoteRepoPath(record.User, sess.Name, sess.RepoNameOr(record.Name)), "--label", label, "--no-focus")
		if err != nil {
			return state.HerdrAttachment{}, err
		}
		out, runErr := runner.Run(args...)
		if runErr != nil {
			// The mutation may have applied. Recover once before returning the
			// uncertain reply so a retry cannot create a duplicate.
			workspace, evidence, found, err = recoverIntendedWorkspace(runner, attachment.Address, label)
			if err != nil || !found {
				return state.HerdrAttachment{}, fmt.Errorf("creating Herdr workspace: %w\n%s", runErr, out)
			}
		} else {
			var reply workspaceCreateReply
			if err := json.Unmarshal([]byte(out), &reply); err != nil {
				return state.HerdrAttachment{}, fmt.Errorf("reading Herdr workspace create reply: %w", err)
			}
			workspace = reply.Result.Workspace
			evidence = workspaceTerminalEvidence{PaneID: reply.Result.RootPane.ID, TerminalID: reply.Result.RootPane.TerminalID}
			found = workspace.ID != "" && evidence.PaneID != "" && evidence.TerminalID != ""
			if !found {
				workspace, evidence, found, err = recoverIntendedWorkspace(runner, attachment.Address, label)
				if err != nil || !found {
					return state.HerdrAttachment{}, fmt.Errorf("created intent workspace but could not recover its ownership evidence")
				}
			}
		}
	}
	proof, err := json.Marshal(evidence)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	attachment.Handle = workspace.ID
	attachment.Proof = state.OwnershipProof{Scheme: workspaceTerminalProof, Data: proof}
	if err := updateAttachment(store, record.Name, sess.Generation, attachment); err != nil {
		return state.HerdrAttachment{}, err
	}
	rename, err := herdrCommand(attachment.Address, "workspace", "rename", attachment.Handle, sess.Name)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	if out, err := runner.Run(rename...); err != nil {
		return state.HerdrAttachment{}, fmt.Errorf("renaming owned Herdr workspace: %w\n%s", err, out)
	}
	attachment.Phase = state.AttachmentActive
	if err := updateAttachment(store, record.Name, sess.Generation, attachment); err != nil {
		return state.HerdrAttachment{}, err
	}
	if err := FocusWorkspace(runner, attachment.Address, attachment.Handle); err != nil {
		return state.HerdrAttachment{}, err
	}
	return attachment, nil
}

func recoverIntendedWorkspace(runner herdrRunner, address state.HerdrAddress, label string) (herdrWorkspace, workspaceTerminalEvidence, bool, error) {
	args, err := herdrCommand(address, "workspace", "list")
	if err != nil {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, err
	}
	out, err := runner.Run(args...)
	if err != nil {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, fmt.Errorf("listing Herdr workspaces: %w\n%s", err, out)
	}
	workspaces, err := parseWorkspaceList(out)
	if err != nil {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, err
	}
	var matches []herdrWorkspace
	for _, workspace := range workspaces {
		if workspace.Label == label {
			matches = append(matches, workspace)
		}
	}
	if len(matches) == 0 {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, nil
	}
	if len(matches) != 1 {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, fmt.Errorf("attachment intent label %q is ambiguous", label)
	}
	panes, err := listOwnershipPanes(runner, address, matches[0].ID)
	if err != nil {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, err
	}
	if len(panes) != 1 || panes[0].TerminalID == "" {
		return herdrWorkspace{}, workspaceTerminalEvidence{}, false, fmt.Errorf("intent workspace %s has ambiguous ownership evidence", matches[0].ID)
	}
	return matches[0], workspaceTerminalEvidence{PaneID: panes[0].ID, TerminalID: panes[0].TerminalID}, true, nil
}

func invalidateFinalRetirementEvidence(sess *state.Session) {
	if sess.Retirement == nil {
		return
	}
	sess.Retirement.Phase = state.RetirementChecking
	clearRetirementFailure(sess.Retirement)
	for key := range sess.Retirement.Effects {
		if !strings.HasPrefix(key, "herdr:") {
			delete(sess.Retirement.Effects, key)
		}
	}
}

func updateAttachment(store *state.Store, instance, generation string, attachment state.HerdrAttachment) error {
	return store.Update(instance, func(record *state.Record) error {
		for i := range record.Sessions {
			if record.Sessions[i].Generation != generation {
				continue
			}
			for j := range record.Sessions[i].Attachments {
				if record.Sessions[i].Attachments[j].ID == attachment.ID {
					record.Sessions[i].Attachments[j] = attachment
					return nil
				}
			}
		}
		return fmt.Errorf("attachment %s generation is no longer active", attachment.ID)
	})
}

// RefreshWorkspaceProof re-records the proof from a pane the workspace has now.
//
// The proof is the workspace's root pane and terminal, and laying out tabs
// can close exactly that pane. Left alone, ownership could then never be
// proved again and the workspace could not be closed. Call it right after a
// layout that follows a successful ownership check: the check is what makes
// the panes read here ours, so refreshing without one would vouch for
// whatever the handle points at. Anything the consumer closes by hand
// between two such calls is still unprovable, which is why retirement does
// not depend on this alone.
func RefreshWorkspaceProof(ctx context.Context, store *state.Store, instance, generation string, attachment state.HerdrAttachment) (state.HerdrAttachment, error) {
	panes, err := listOwnershipPanes(attachmentHerdrRunner(ctx), attachment.Address, attachment.Handle)
	if err != nil {
		return attachment, err
	}
	if len(panes) == 0 {
		return attachment, fmt.Errorf("workspace %s has no panes to record ownership from", attachment.Handle)
	}
	proof, err := json.Marshal(workspaceTerminalEvidence{PaneID: panes[0].ID, TerminalID: panes[0].TerminalID})
	if err != nil {
		return attachment, err
	}
	attachment.Proof = state.OwnershipProof{Scheme: workspaceTerminalProof, Data: proof}
	return attachment, updateAttachment(store, instance, generation, attachment)
}

// BeginDirectSessionAttachment records authority before the interactive
// client can create its named server. The caller marks it active only after
// Herdr confirms that attachment succeeded.
func BeginDirectSessionAttachment(ctx context.Context, store *state.Store, instance, session string) (state.HerdrAttachment, error) {
	instanceRelease, err := store.LockInstance(ctx, instance, false)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	defer func() { _ = instanceRelease() }()
	sessionRelease, err := store.LockSession(ctx, instance, session)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	defer func() { _ = sessionRelease() }()
	record, ok, err := store.Get(instance)
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	if !ok {
		return state.HerdrAttachment{}, fmt.Errorf("instance %s not found", instance)
	}
	sess, ok := record.FindSession(session)
	if !ok {
		return state.HerdrAttachment{}, fmt.Errorf("instance %s has no session %q", instance, session)
	}
	if !sess.AttachmentsKnown {
		return state.HerdrAttachment{}, fmt.Errorf("session %s predates attachment ownership tracking; inspect its exact Herdr resource and confirm adoption interactively before attaching", session)
	}
	if sess.Retirement != nil && sess.Retirement.Phase != state.RetirementPaused {
		return state.HerdrAttachment{}, fmt.Errorf("session %s retirement is %s; resume %s instead of attaching", session, sess.Retirement.Phase, sess.Retirement.Operation)
	}
	for _, attachment := range sess.Attachments {
		if attachment.Kind == state.AttachmentNamedSession && attachment.Phase != state.AttachmentClosed {
			return attachment, nil
		}
	}
	id, err := state.NewSessionGeneration()
	if err != nil {
		return state.HerdrAttachment{}, err
	}
	evidence, _ := json.Marshal(namedSessionEvidence{Session: session, Token: id})
	attachment := state.HerdrAttachment{
		ID: id, Kind: state.AttachmentNamedSession,
		Address: state.HerdrAddress{Session: session, ConfigRoot: herdrConfigRoot},
		Handle:  session, Proof: state.OwnershipProof{Scheme: namedSessionIntentProof, Data: evidence},
		Owned: true, Phase: state.AttachmentCreating,
	}
	if err := store.Update(instance, func(latest *state.Record) error {
		current, ok := latest.FindSession(session)
		if !ok || current.Generation != sess.Generation {
			return fmt.Errorf("session %s generation changed before direct Herdr attach", session)
		}
		current.Attachments = append(current.Attachments, attachment)
		invalidateFinalRetirementEvidence(&current)
		latest.PutSession(current)
		return nil
	}); err != nil {
		return state.HerdrAttachment{}, err
	}
	return attachment, nil
}

func CompleteDirectSessionAttachment(store *state.Store, instance, generation string, attachment state.HerdrAttachment) error {
	attachment.Phase = state.AttachmentActive
	return updateAttachment(store, instance, generation, attachment)
}

// CloseSessionAttachment verifies the exact server and opaque proof, closes
// the resource, and observes authoritative absence. It never removes the
// shared saved machine.
func CloseSessionAttachment(ctx context.Context, record state.Record, attachment state.HerdrAttachment) error {
	if !attachment.Owned {
		return fmt.Errorf("attachment %s is not owned by bivouac", attachment.ID)
	}
	if attachment.Kind == state.AttachmentNamedSession {
		return closeNamedSessionAttachment(ctx, record, attachment)
	}
	if attachment.Kind != state.AttachmentWorkspace {
		return fmt.Errorf("attachment %s kind %s has no supported close adapter", attachment.ID, attachment.Kind)
	}
	runner := attachmentHerdrRunner(record.ConnectionContext(ctx))
	if err := verifyMachineAddress(runner, record, attachment.Address); err != nil {
		return err
	}
	present, err := workspacePresent(runner, attachment.Address, attachment.Handle)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if err := verifyWorkspaceOwnership(runner, attachment); err != nil {
		return err
	}
	args, err := herdrCommand(attachment.Address, "workspace", "close", attachment.Handle)
	if err != nil {
		return err
	}
	out, err := runner.Run(args...)
	if err != nil {
		// Lost close replies are reconciled only by an authoritative list.
		present, observeErr := workspacePresent(runner, attachment.Address, attachment.Handle)
		if observeErr == nil && !present {
			return nil
		}
		return errors.Join(fmt.Errorf("closing Herdr workspace: %w\n%s", err, out), observeErr)
	}
	present, err = workspacePresent(runner, attachment.Address, attachment.Handle)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("Herdr reported workspace %s closed but it is still present", attachment.Handle)
	}
	return nil
}

func closeNamedSessionAttachment(ctx context.Context, record state.Record, attachment state.HerdrAttachment) error {
	if err := validateHerdrAddress(attachment.Address); err != nil {
		return err
	}
	if attachment.Proof.Scheme != namedSessionIntentProof {
		return fmt.Errorf("attachment %s has unsupported named-session proof %q", attachment.ID, attachment.Proof.Scheme)
	}
	var evidence namedSessionEvidence
	if err := json.Unmarshal(attachment.Proof.Data, &evidence); err != nil || evidence.Token != attachment.ID || evidence.Session != attachment.Address.Session || attachment.Handle != evidence.Session {
		return fmt.Errorf("attachment %s has malformed named-session proof", attachment.ID)
	}
	client, err := attachmentRemoteConnect(record.ConnectionContext(ctx), record.IP, record.User)
	if err != nil {
		return fmt.Errorf("connecting to inspect named Herdr session: %w", err)
	}
	defer func() { _ = client.Close() }()
	present, err := namedSessionPresent(client, evidence.Session)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if out, err := client.Run(namedSessionCommand("stop", evidence.Session)); err != nil {
		return fmt.Errorf("stopping owned Herdr session %s: %w\n%s", evidence.Session, err, out)
	}
	if out, err := client.Run(namedSessionCommand("delete", evidence.Session)); err != nil {
		return fmt.Errorf("deleting owned Herdr session %s: %w\n%s", evidence.Session, err, out)
	}
	present, err = namedSessionPresent(client, evidence.Session)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("Herdr session %s is still present after delete", evidence.Session)
	}
	return nil
}

func namedSessionCommand(verb, session string) string {
	return shellcmd.LoginShell("herdr session " + verb + " " + shellcmd.Quote(session))
}

func namedSessionPresent(runner remoteRunner, session string) (bool, error) {
	out, err := runner.Run(shellcmd.LoginShell("herdr session list --json"))
	if err != nil {
		return false, fmt.Errorf("listing named Herdr sessions: %w\n%s", err, out)
	}
	var sessions []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		return false, fmt.Errorf("reading named Herdr sessions: %w", err)
	}
	for _, candidate := range sessions {
		if candidate.Name == session {
			return true, nil
		}
	}
	return false, nil
}

func verifyMachineAddress(runner herdrRunner, record state.Record, address state.HerdrAddress) error {
	if err := validateHerdrAddress(address); err != nil {
		return err
	}
	if record.HerdrMachineID != address.MachineID {
		return fmt.Errorf("attachment machine %s is not the instance-owned machine %s", address.MachineID, record.HerdrMachineID)
	}
	profiles, err := listMachines(runner)
	if err != nil {
		return err
	}
	profile, ok := findMachineByID(profiles, address.MachineID)
	if !ok {
		return fmt.Errorf("recorded Herdr machine %s is absent; ownership cannot be verified", address.MachineID)
	}
	if profile.Session != address.Session {
		return fmt.Errorf("Herdr machine %s now targets session %s instead of %s", address.MachineID, profile.Session, address.Session)
	}
	if address.ConfigRoot != herdrConfigRoot {
		return fmt.Errorf("unsupported Herdr config namespace %s", address.ConfigRoot)
	}
	return nil
}

func verifyWorkspaceOwnership(runner herdrRunner, attachment state.HerdrAttachment) error {
	if attachment.Proof.Scheme != workspaceTerminalProof {
		return fmt.Errorf("attachment %s has unsupported ownership proof %q", attachment.ID, attachment.Proof.Scheme)
	}
	var evidence workspaceTerminalEvidence
	if err := json.Unmarshal(attachment.Proof.Data, &evidence); err != nil || evidence.PaneID == "" || evidence.TerminalID == "" {
		return fmt.Errorf("attachment %s has malformed ownership proof", attachment.ID)
	}
	panes, err := listOwnershipPanes(runner, attachment.Address, attachment.Handle)
	if err != nil {
		return err
	}
	for _, pane := range panes {
		if pane.ID == evidence.PaneID && pane.TerminalID == evidence.TerminalID {
			return nil
		}
	}
	return fmt.Errorf("workspace %s no longer matches attachment %s ownership proof (its original pane was closed or replaced): %w", attachment.Handle, attachment.ID, errOwnershipUnproven)
}

func listOwnershipPanes(runner herdrRunner, address state.HerdrAddress, workspace string) ([]ownershipPane, error) {
	args, err := herdrCommand(address, "pane", "list", "--workspace", workspace)
	if err != nil {
		return nil, err
	}
	out, err := runner.Run(args...)
	if err != nil {
		return nil, fmt.Errorf("observing Herdr workspace %s: %w\n%s", workspace, err, out)
	}
	var reply struct {
		Result struct {
			Panes []ownershipPane `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		return nil, fmt.Errorf("reading Herdr panes: %w", err)
	}
	return reply.Result.Panes, nil
}

func workspacePresent(runner herdrRunner, address state.HerdrAddress, handle string) (bool, error) {
	args, err := herdrCommand(address, "workspace", "list")
	if err != nil {
		return false, err
	}
	out, err := runner.Run(args...)
	if err != nil {
		return false, fmt.Errorf("observing Herdr workspace: %w\n%s", err, out)
	}
	workspaces, err := parseWorkspaceList(out)
	if err != nil {
		return false, err
	}
	for _, workspace := range workspaces {
		if workspace.ID == handle {
			return true, nil
		}
	}
	return false, nil
}

func attachmentIntentLabel(id string) string {
	return attachmentIntentLabelPrefix + strings.ToLower(id)
}
