package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/matthewalunni/dispatch/internal/herdrx"
	"github.com/matthewalunni/dispatch/internal/naming"
	"github.com/matthewalunni/dispatch/internal/store"
)

// Report records a task's latest self-reported status line and progress.
func (a *App) Report(ctx context.Context, ref, line string, done, total int) error {
	task, err := a.store.Resolve(ctx, ref)
	if err != nil {
		return err
	}
	if done < 0 || total < 0 {
		return errors.New("done and total cannot be negative")
	}
	return a.store.SetReport(ctx, store.Report{TaskID: task.ID, Line: strings.TrimSpace(line), Done: done, Total: total})
}

// FileAsk validates an ask and stores it against a live task, filling in its
// id, task, state and creation time.
func (a *App) FileAsk(ctx context.Context, ref string, ask store.Ask) (store.Ask, error) {
	task, err := a.store.Resolve(ctx, ref)
	if err != nil {
		return store.Ask{}, err
	}
	if task.Status.Terminal() {
		return store.Ask{}, fmt.Errorf("task %s is %s and cannot take new asks", task.ShortID(), task.Status)
	}
	ask.Title = strings.TrimSpace(ask.Title)
	if ask.Title == "" {
		return store.Ask{}, errors.New("an ask needs a title")
	}
	switch ask.Kind {
	case store.AskDecision:
		if len(ask.Options) < 2 {
			return store.Ask{}, errors.New("a decision needs at least two options")
		}
		for _, opt := range ask.Options {
			if strings.TrimSpace(opt.Label) == "" {
				return store.Ask{}, errors.New("every option needs a label")
			}
		}
		if ask.Recommended < 0 || ask.Recommended >= len(ask.Options) {
			return store.Ask{}, fmt.Errorf("recommended option %d is out of range for %d options", ask.Recommended, len(ask.Options))
		}
	case store.AskChore:
		ask.Recommended = -1
	default:
		return store.Ask{}, fmt.Errorf("unknown ask kind %q", ask.Kind)
	}

	now := a.now()
	ask.ID = naming.AskID(now)
	ask.TaskID = task.ID
	ask.State = store.AskOpen
	ask.CreatedAt = now
	ask.Answer, ask.AnsweredAt, ask.DeliveredAt = "", nil, nil
	if err := a.store.CreateAsk(ctx, &ask); err != nil {
		return store.Ask{}, err
	}
	return ask, nil
}

// WithdrawAsk retracts an ask the agent no longer needs answered. Only an open
// ask can be withdrawn; once answered, the human's reply is on its way.
func (a *App) WithdrawAsk(ctx context.Context, askID string) error {
	ask, err := a.store.GetAsk(ctx, askID)
	if err != nil {
		return err
	}
	if ask.State != store.AskOpen {
		return fmt.Errorf("ask %s is %s and cannot be withdrawn", ask.ID, ask.State)
	}
	err = a.store.TransitionAsk(ctx, ask.ID, store.AskOpen, store.AskWithdrawn, "")
	if errors.Is(err, store.ErrAskState) {
		return fmt.Errorf("ask %s was answered or withdrawn first", ask.ID)
	}
	return err
}

// Answer is a human's reply to an ask.
type Answer struct {
	Option    int // chosen option, -1 for none
	Note      string
	ChoreDone bool
}

// AnswerResult reports what happened to an answer. Delivered is false when it
// is stored but not yet in the agent's session; Reason says why.
type AnswerResult struct {
	Ask       store.Ask
	Delivered bool
	Reason    string
}

// AnswerPrompt is the text typed into the agent's session for an answer.
func AnswerPrompt(ask store.Ask, ans Answer) string {
	note := strings.TrimSpace(ans.Note)
	if ask.Kind == store.AskChore {
		text := fmt.Sprintf("[dispatch] %q is done.", ask.Title)
		if note != "" {
			text += " " + note
		}
		return text + " Carry on."
	}
	if ans.Option >= 0 && ans.Option < len(ask.Options) {
		text := fmt.Sprintf("[dispatch] Your question %q was answered: %s.", ask.Title, ask.Options[ans.Option].Label)
		if note != "" {
			text += " Note: " + note
		}
		return text + " Carry on."
	}
	return fmt.Sprintf("[dispatch] Reply to %q: %s Carry on.", ask.Title, note)
}

// AnswerAsk records a human's answer, then tries to deliver it. The answer is
// stored first so it survives an agent that cannot take it right now; it is
// marked delivered only once the agent's session accepted the prompt.
func (a *App) AnswerAsk(ctx context.Context, askID string, ans Answer) (*AnswerResult, error) {
	ask, err := a.store.GetAsk(ctx, askID)
	if err != nil {
		return nil, err
	}
	if ask.State != store.AskOpen {
		return nil, fmt.Errorf("ask %s is %s and cannot be answered", ask.ID, ask.State)
	}
	switch {
	case ask.Kind == store.AskChore:
		if !ans.ChoreDone {
			return nil, errors.New("a chore is answered by marking it done")
		}
	case ans.Option >= len(ask.Options):
		return nil, fmt.Errorf("option %d is out of range for %d options", ans.Option, len(ask.Options))
	case ans.Option < 0 && strings.TrimSpace(ans.Note) == "":
		return nil, errors.New("choose an option or write a note")
	}
	task, err := a.store.Get(ctx, ask.TaskID)
	if err != nil {
		return nil, err
	}

	text := AnswerPrompt(ask, ans)
	if err := a.store.TransitionAsk(ctx, ask.ID, store.AskOpen, store.AskAnswered, text); err != nil {
		if errors.Is(err, store.ErrAskState) {
			return nil, fmt.Errorf("ask %s was already answered or withdrawn", ask.ID)
		}
		return nil, err
	}
	reason, delivered := a.deliver(ctx, task, ask.ID, text)
	stored, err := a.store.GetAsk(ctx, ask.ID)
	if err != nil {
		return nil, err
	}
	return &AnswerResult{Ask: stored, Delivered: delivered, Reason: reason}, nil
}

// deliver types an answer into the task's session and marks the ask delivered
// on success. On failure the ask stays answered and the reason is returned.
func (a *App) deliver(ctx context.Context, task store.Task, askID, text string) (string, bool) {
	if err := a.herdr.PromptAgent(ctx, task.SessionRef(), text); err != nil {
		if herdrx.IsCode(err, "agent_blocked") {
			return "the agent is waiting on a prompt; the answer will be delivered when it is free", false
		}
		return err.Error(), false
	}
	// The prompt is already in the session, so a failed write is retried once
	// rather than left to trigger a second prompt on the next hydrate.
	err := a.store.TransitionAsk(ctx, askID, store.AskAnswered, store.AskDelivered, "")
	if err != nil && !errors.Is(err, store.ErrAskState) {
		err = a.store.TransitionAsk(ctx, askID, store.AskAnswered, store.AskDelivered, "")
	}
	if err != nil && !errors.Is(err, store.ErrAskState) {
		return "answer delivered but could not be recorded: " + err.Error(), true
	}
	return "", true
}

// deliverPending retries answers that could not be delivered earlier. It makes
// one attempt per answer and skips agents that are gone or blocked, so a
// stuck session never stalls a hydrate. Best effort: failures stay answered.
func (a *App) deliverPending(ctx context.Context, live map[string]herdrx.Agent) {
	asks, err := a.store.ListAsks(ctx, store.AskFilter{States: []store.AskState{store.AskAnswered}})
	if err != nil {
		return
	}
	for _, ask := range asks {
		task, err := a.store.Get(ctx, ask.TaskID)
		// An ended task's agent name or pane may now belong to someone else.
		if err != nil || task.Status.Terminal() {
			continue
		}
		agent, ok := lookupAgent(live, task)
		if !ok || agent.Status == herdrx.StatusBlocked {
			continue
		}
		a.deliver(ctx, task, ask.ID, ask.Answer)
	}
}
