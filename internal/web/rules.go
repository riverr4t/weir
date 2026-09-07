package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/riverr4t/weir/internal/rules"
	"github.com/riverr4t/weir/internal/store"
)

type ruleRow struct {
	store.Rule
	Conds   []rules.Condition
	Last    *store.RuleRun
	Summary string
}

type rulesModel struct {
	Rules  []ruleRow
	Kinds  any
	Scopes []rules.Scope
	Users  []string
}

type ruleEditModel struct {
	Rule   store.Rule
	Conds  []rules.Condition
	Kinds  any
	Scopes []rules.Scope
	Users  []string
	Runs   []store.RuleRun
	Error  string
	IsNew  bool
}

type runModel struct {
	Rule    store.Rule
	Run     store.RuleRun
	Matches []store.RuleMatch
}

func describe(cs []rules.Condition) string {
	var parts []string
	for _, c := range cs {
		switch c.Kind {
		case "watched_by_all":
			parts = append(parts, "watched by everyone, untouched "+strconv.Itoa(c.Days)+" d")
		case "watched_by":
			parts = append(parts, "watched by "+strings.Join(c.Users, ", ")+", untouched "+strconv.Itoa(c.Days)+" d")
		case "never_played":
			parts = append(parts, "never played, added over "+strconv.Itoa(c.Days)+" d ago")
		case "requester_done":
			parts = append(parts, "requester finished it, "+strconv.Itoa(c.Days)+" d quiet")
		case "ended_and_finished":
			parts = append(parts, "ended and finished")
		case "unmonitored":
			parts = append(parts, "unmonitored")
		case "larger_than":
			parts = append(parts, "over "+strconv.FormatFloat(c.GiB, 'f', -1, 64)+" GiB")
		case "added_before":
			parts = append(parts, "added over "+strconv.Itoa(c.Days)+" d ago")
		case "tagged":
			parts = append(parts, "tagged "+c.Tag)
		case "not_tagged":
			parts = append(parts, "not tagged "+c.Tag)
		}
	}
	return strings.Join(parts, " and ")
}

func (s *Server) jellyfinUsers() []string {
	j := s.jellyfin()
	if j == nil {
		return nil
	}
	var out []string
	for _, u := range j.PlayState.Get().Data.Users {
		out = append(out, u.Name)
	}
	return out
}

func (s *Server) rulesPage(w http.ResponseWriter, r *http.Request) {
	all, err := s.DB.Rules(r.Context())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	last, _ := s.DB.LastRuns(r.Context())
	m := &rulesModel{Kinds: rules.Kinds, Scopes: rules.Scopes, Users: s.jellyfinUsers()}
	for _, ru := range all {
		row := ruleRow{Rule: ru}
		row.Conds, _ = rules.ParseConditions(ru.Conditions)
		row.Summary = describe(row.Conds)
		if lr, ok := last[ru.ID]; ok {
			lr := lr
			row.Last = &lr
		}
		m.Rules = append(m.Rules, row)
	}
	p := s.base("Rules", "rules")
	p.Rules = m
	s.render(w, "rules.html", p)
}

func (s *Server) ruleEdit(w http.ResponseWriter, r *http.Request) {
	m := &ruleEditModel{Kinds: rules.Kinds, Scopes: rules.Scopes, Users: s.jellyfinUsers(), IsNew: true}
	m.Rule = store.Rule{Scope: "movie", Enabled: true}
	if idStr := r.PathValue("id"); idStr != "" {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		ru, err := s.DB.Rule(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		m.Rule, m.IsNew = ru, false
		m.Conds, _ = rules.ParseConditions(ru.Conditions)
		m.Runs, _ = s.DB.Runs(r.Context(), id, 10)
	}
	p := s.base("Rule", "rules")
	p.RuleEdit = m
	s.render(w, "rule-edit.html", p)
}

// POST /rules and POST /rules/{id}: the editor submits one form with a JSON conditions field.
func (s *Server) ruleSave(w http.ResponseWriter, r *http.Request) {
	var ru store.Rule
	if idStr := r.PathValue("id"); idStr != "" {
		ru.ID, _ = strconv.ParseInt(idStr, 10, 64)
	}
	ru.Name = strings.TrimSpace(r.FormValue("name"))
	ru.Scope = r.FormValue("scope")
	ru.Tag = strings.TrimSpace(strings.TrimPrefix(r.FormValue("tag"), "weir-"))
	ru.Enabled = r.FormValue("enabled") == "on" || r.FormValue("enabled") == "1"
	ru.Conditions = json.RawMessage(r.FormValue("conditions"))
	fail := func(msg string) {
		m := &ruleEditModel{Rule: ru, Kinds: rules.Kinds, Scopes: rules.Scopes, Users: s.jellyfinUsers(), Error: msg, IsNew: ru.ID == 0}
		m.Conds, _ = rules.ParseConditions(ru.Conditions)
		p := s.base("Rule", "rules")
		p.RuleEdit = m
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, "rule-edit.html", p)
	}
	if ru.Name == "" {
		fail("Give the rule a name.")
		return
	}
	valid := false
	for _, sc := range rules.Scopes {
		if string(sc) == ru.Scope {
			valid = true
		}
	}
	if !valid {
		fail("Pick a scope.")
		return
	}
	conds, err := rules.ParseConditions(ru.Conditions)
	if err != nil {
		fail("Conditions: " + err.Error())
		return
	}
	if len(conds) == 0 {
		fail("Add at least one condition.")
		return
	}
	for _, c := range conds {
		if !rules.KindAllowed(c.Kind, rules.Scope(ru.Scope)) {
			fail("The condition \"" + c.Kind + "\" does not apply to " + ru.Scope + ".")
			return
		}
	}
	ru.Conditions = rules.ConditionsJSON(conds)
	id, err := s.DB.SaveRule(r.Context(), ru, s.Now())
	if err != nil {
		fail(err.Error())
		return
	}
	s.log(r.Context(), r, "rule.save", "weir", ru.Name, map[string]any{"id": id, "scope": ru.Scope, "tag": ru.Tag}, nil)
	http.Redirect(w, r, "/rules/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (s *Server) ruleToggle(enable bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err := s.DB.SetRuleEnabled(r.Context(), id, enable, s.Now()); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/rules", http.StatusSeeOther)
	})
}

func (s *Server) ruleDelete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ru, err := s.DB.Rule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.DB.DeleteRule(r.Context(), id); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.log(r.Context(), r, "rule.delete", "weir", ru.Name, map[string]any{"id": id}, nil)
	http.Redirect(w, r, "/rules", http.StatusSeeOther)
}

func (s *Server) ruleRun(w http.ResponseWriter, r *http.Request) {
	if s.Rules == nil {
		http.Error(w, "rules engine not running", http.StatusServiceUnavailable)
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ru, err := s.DB.Rule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	runID, err := s.Rules.RunRule(r.Context(), ru, time.Now())
	s.log(r.Context(), r, "rule.run", "weir", ru.Name, map[string]any{"id": id, "run": runID}, err)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, "/rules/"+strconv.FormatInt(id, 10)+"/runs/"+strconv.FormatInt(runID, 10), http.StatusSeeOther)
}

func (s *Server) ruleRunPage(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	runID, _ := strconv.ParseInt(r.PathValue("run"), 10, 64)
	ru, err := s.DB.Rule(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	run, ms, err := s.DB.Run(r.Context(), runID)
	if err != nil || run.RuleID != id {
		http.NotFound(w, r)
		return
	}
	p := s.base("Rule run", "rules")
	p.RuleRun = &runModel{Rule: ru, Run: run, Matches: ms}
	s.render(w, "rule-run.html", p)
}

func (s *Server) apiRules(w http.ResponseWriter, r *http.Request) {
	all, err := s.DB.Rules(r.Context())
	if err != nil {
		writeJSON(w, s.Now(), nil, err)
		return
	}
	last, _ := s.DB.LastRuns(r.Context())
	type out struct {
		store.Rule
		Last *store.RuleRun `json:"last,omitempty"`
	}
	var rows []out
	for _, ru := range all {
		o := out{Rule: ru}
		if lr, ok := last[ru.ID]; ok {
			lr := lr
			o.Last = &lr
		}
		rows = append(rows, o)
	}
	writeJSON(w, s.Now(), rows, nil)
}
