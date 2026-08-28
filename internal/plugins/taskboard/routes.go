package taskboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrCardNotFound), errors.Is(err, ErrProjectNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrVersionConflict), errors.Is(err, ErrDoneIsHumanOnly),
		errors.Is(err, ErrCardHeld), errors.Is(err, ErrRunningExecution),
		errors.Is(err, ErrProjectHasCards), errors.Is(err, ErrBuiltInProject),
		errors.Is(err, ErrInvalidTransition):
		status = http.StatusConflict
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func decodeBody(r *http.Request, target any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(target); err != nil {
		return fmt.Errorf("taskboard: invalid JSON body: %w", err)
	}
	return nil
}

func pathID(r *http.Request) string { return strings.TrimSpace(r.PathValue("id")) }

// ---- handlers ----

func (p *Plugin) handleListProjects(w http.ResponseWriter, r *http.Request) (any, error) {
	return map[string]any{"projects": p.ledger.ListProjects()}, nil
}

type projectBody struct {
	Name    string `json:"name"`
	RootDir string `json:"root_dir"`
}

func (p *Plugin) handleCreateProject(w http.ResponseWriter, r *http.Request) (any, error) {
	var body projectBody
	if err := decodeBody(r, &body); err != nil {
		return nil, err
	}
	project, err := p.ledger.CreateProject(body.Name, body.RootDir)
	if err != nil {
		return nil, err
	}
	return map[string]any{"project": project}, nil
}

func (p *Plugin) handleListCards(w http.ResponseWriter, r *http.Request) (any, error) {
	q := r.URL.Query()
	cards := p.ledger.ListCards(CardFilter{
		ProjectID: q.Get("project"),
		Status:    q.Get("status"),
		Urgency:   q.Get("urgency"),
	})
	return map[string]any{"cards": cards, "count": len(cards)}, nil
}

func (p *Plugin) handleCreateCard(w http.ResponseWriter, r *http.Request) (any, error) {
	var input CreateCardInput
	if err := decodeBody(r, &input); err != nil {
		return nil, err
	}
	input.CreatedBy = "human"
	card, err := p.ledger.CreateCard(input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"card": card}, nil
}

func (p *Plugin) handleGetCard(w http.ResponseWriter, r *http.Request) (any, error) {
	card, err := p.ledger.GetCard(pathID(r))
	if err != nil {
		return nil, err
	}
	return map[string]any{"card": card}, nil
}

// patchBody is the human card-mutation envelope; action picks the ledger op.
type patchBody struct {
	Action  string `json:"action"` // update|move|complete|reject|checklist
	Version int    `json:"version"`
	Actor   string `json:"actor,omitempty"`
	// update fields
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Prompt      *string `json:"prompt"`
	Urgency     *string `json:"urgency"`
	Blocked     *bool   `json:"blocked"`
	TemplateID  *string `json:"template_id"`
	// move
	To string `json:"to"`
	// complete
	Force bool `json:"force"`
	// reject
	Reason string `json:"reason"`
	// checklist: add by text; check/uncheck by index
	CheckAction string `json:"check_action"`
	Index       *int   `json:"index"`
	Text        string `json:"text"`
	Evidence    string `json:"evidence"`
}

func (b patchBody) actor() string {
	if actor := strings.TrimSpace(b.Actor); actor != "" {
		return actor
	}
	return "human"
}

func (p *Plugin) handlePatchCard(w http.ResponseWriter, r *http.Request) (any, error) {
	var body patchBody
	if err := decodeBody(r, &body); err != nil {
		return nil, err
	}
	id := pathID(r)
	actor := body.actor()
	var card Card
	var err error
	switch strings.TrimSpace(body.Action) {
	case "update":
		card, err = p.ledger.UpdateCard(id, body.Version, actor, UpdateCardInput{
			Title: body.Title, Description: body.Description, Prompt: body.Prompt,
			Urgency: body.Urgency, Blocked: body.Blocked, TemplateID: body.TemplateID,
		})
	case "move":
		card, err = p.ledger.MoveCard(id, body.Version, body.To, actor)
	case "complete":
		card, err = p.ledger.CompleteCard(id, body.Version, actor, body.Force)
	case "reject":
		card, err = p.ledger.RejectCard(id, body.Version, actor, body.Reason)
	case "checklist":
		switch strings.TrimSpace(body.CheckAction) {
		case "add":
			card, err = p.ledger.ChecklistAdd(id, body.Version, actor, body.Text)
		case "check":
			if body.Index == nil {
				return nil, fmt.Errorf("taskboard: check requires item index")
			}
			card, err = p.ledger.ChecklistCheck(id, body.Version, actor, *body.Index, body.Evidence)
		case "uncheck":
			if body.Index == nil {
				return nil, fmt.Errorf("taskboard: uncheck requires item index")
			}
			card, err = p.ledger.ChecklistUncheck(id, body.Version, actor, *body.Index)
		default:
			return nil, fmt.Errorf("taskboard: unknown checklist action %q", body.CheckAction)
		}
	case "":
		return nil, fmt.Errorf("taskboard: patch action is required")
	default:
		return nil, fmt.Errorf("taskboard: unknown patch action %q", body.Action)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"card": card}, nil
}

func (p *Plugin) handleDeleteCard(w http.ResponseWriter, r *http.Request) (any, error) {
	card, err := p.ledger.GetCard(pathID(r))
	if err != nil {
		return nil, err
	}
	version, ok := intQuery(r, "version")
	if !ok {
		version = card.Version
	}
	deleted, err := p.ledger.SoftDeleteCard(card.ID, version, "human")
	if err != nil {
		return nil, err
	}
	return map[string]any{"card_id": deleted.ID, "deleted": true}, nil
}

func intQuery(r *http.Request, key string) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return 0, false
	}
	var v int
	if _, err := fmt.Sscanf(raw, "%d", &v); err != nil {
		return 0, false
	}
	return v, true
}

func (p *Plugin) handleExecuteCard(w http.ResponseWriter, r *http.Request) (any, error) {
	if p.executor == nil {
		return nil, fmt.Errorf("taskboard: executor not configured")
	}
	card, err := p.ledger.GetCard(pathID(r))
	if err != nil {
		return nil, err
	}
	if card.Deleted {
		return nil, fmt.Errorf("taskboard: card %s is deleted", card.ID)
	}
	if card.HasRunningExecution() {
		return nil, ErrRunningExecution
	}
	executionID, sessionID, err := p.executor.Execute(r.Context(), card)
	if err != nil {
		return nil, err
	}
	return map[string]any{"execution_id": executionID, "session_id": sessionID}, nil
}
