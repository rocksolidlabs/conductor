package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/rocksolidlabs/conductor/internal/version"
)

// Codex's usage, read through `codex app-server` on stdio (marked
// experimental in `codex --help`; its two methods are in the stable
// schema, `app-server generate-json-schema` without --experimental, and
// cost no turn). Checked on 0.163.0, by hand and by the recipes tier; a
// newer Codex is read, a changed format failing the read with its field.
const (
	codexUsageQualified  = "0.163.0"
	codexUsageMinVersion = "0.163.0"
)

var codexAppServerArgs = []string{"app-server", "--listen", "stdio://"}

// codexPlans are the plan types reported (app-server's PlanType, 0.163.0;
// the recipes tier fails when the schema names one not here); another is
// not reported.
var codexPlans = map[string]bool{"free": true, "go": true, "plus": true, "pro": true, "prolite": true, "promax": true, "team": true,
	"self_serve_business_prolite": true, "self_serve_business_usage_based": true, "business": true, "ent26": true,
	"enterprise_cbp_automation": true, "enterprise_cbp_usage_based": true, "enterprise": true, "edu": true, "edu_plus": true, "edu_pro": true, "unknown": true}

// codexReached are the rateLimitReachedType values Conductor knows
// (RateLimitReachedType, 0.163.0, held to the schema as codexPlans is);
// another fails the read.
var codexReached = map[string]bool{"rate_limit_reached": true, "workspace_owner_credits_depleted": true, "workspace_member_credits_depleted": true,
	"workspace_owner_usage_limit_reached": true, "workspace_member_usage_limit_reached": true}

// codexMaxReset is the last reset time a report carries: JSON's last
// four-digit year. A later one (or one in milliseconds) is no time.
var codexMaxReset = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC).Unix()

// codexUsageMethods is everything the reader sends, in this order:
// config/read, which would answer the person's configuration (it can hold
// a secret), is never among them.
var codexUsageMethods = [4]string{"initialize", "initialized", "account/read", "account/rateLimits/read"}

func codexUsageReader() *UsageReader {
	return &UsageReader{Qualified: codexUsageQualified, MinVersion: codexUsageMinVersion, Read: readCodexUsage}
}

type rpcReply struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	// Error is the refusal; its message is never read, only its code.
	Error *struct {
		Code *int `json:"code"`
	} `json:"error"`
}

// codexAccount is what is kept of account/read: never the email, never
// the workspace's routing or ids.
type codexAccount struct {
	Account *struct {
		Type     string `json:"type"`
		PlanType string `json:"planType"`
	} `json:"account"`
	RequiresOpenaiAuth *bool `json:"requiresOpenaiAuth"`
}

// codexLimits is what is kept of account/rateLimits/read: never its
// accountId, its reset credits or its banner.
type codexLimits struct {
	RateLimits           *codexSnapshot           `json:"rateLimits"`
	ByLimitID            map[string]codexSnapshot `json:"rateLimitsByLimitId"`
	OrdinaryUsageAllowed *bool                    `json:"ordinaryUsageAllowed"`
}

type codexSnapshot struct {
	LimitID   *string      `json:"limitId"`
	LimitName *string      `json:"limitName"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
	Credits   *struct {
		HasCredits *bool   `json:"hasCredits"`
		Unlimited  bool    `json:"unlimited"`
		Balance    *string `json:"balance"`
	} `json:"credits"`
	PlanType             *string `json:"planType"`
	RateLimitReachedType *string `json:"rateLimitReachedType"`
	SpendControlReached  *bool   `json:"spendControlReached"`
}

type codexWindow struct {
	UsedPercent        *json.Number `json:"usedPercent"`
	WindowDurationMins *int64       `json:"windowDurationMins"`
	ResetsAt           *int64       `json:"resetsAt"`
}

func readCodexUsage(ctx context.Context, in UsageInput) (rep UsageReport) {
	defer func() { rep.ReadAt = time.Now(); rep.Words = clip(rep.Words, usageMaxWords) }()
	ctx, cancel := withUsageTimeout(ctx, codexUsageTimeout)
	defer cancel()
	p, v, stop := qualify(ctx, in, codexIdentity, "Codex", codexUsageMinVersion)
	if stop != nil {
		return *stop
	}
	fail := func(words string) UsageReport { return UsageReport{State: UsageError, Words: words, Version: v} }
	proc, err := startUsage(ctx, p.with(codexAppServerArgs...), in, true, false)
	if err != nil {
		return fail("Codex could not be started")
	}
	defer proc.end()
	c := codexClient{proc: proc}
	var acct codexAccount
	if w := c.call(1, codexUsageMethods[0], map[string]any{"clientInfo": map[string]string{"name": "conductor", "version": version.Version}}, nil); w != "" {
		return fail(w)
	}
	if w := c.send(map[string]any{"method": codexUsageMethods[1]}); w != "" {
		return fail(w)
	}
	if w := c.call(2, codexUsageMethods[2], map[string]any{}, &acct); w != "" {
		return fail(w)
	}
	switch {
	case acct.Account == nil && acct.RequiresOpenaiAuth != nil && *acct.RequiresOpenaiAuth:
		return UsageReport{State: UsageSignedOut, Words: "not signed in to Codex", Version: v}
	case acct.Account == nil:
		return fail("Codex reports no OpenAI account, and needs none: its usage is not read")
	case acct.Account.Type == "apiKey":
		return UsageReport{State: UsageKeyPaid, Words: "signed in with an API key: no usage windows are reported", Version: v}
	case acct.Account.Type == "amazonBedrock":
		return UsageReport{State: UsageKeyPaid, Words: "paid through Amazon Bedrock: no usage windows are reported", Version: v}
	case acct.Account.Type != "chatgpt":
		return fail("account/read names an account type Conductor does not know: its usage is not read")
	}
	var lim codexLimits
	if w := c.call(3, codexUsageMethods[3], nil, &lim); w != "" {
		return fail(w)
	}
	proc.end()
	rep, err = codexReport(lim)
	if err != nil {
		return fail("Codex's rate limits: " + err.Error())
	}
	rep.Version = v
	if codexPlans[acct.Account.PlanType] {
		rep.Plan = acct.Account.PlanType
	} else if acct.Account.PlanType == "" {
		rep.Plan = codexPlan(lim)
	}
	return rep
}

// codexClient writes requests and reads their replies by id, skipping
// notifications and the server's own requests.
type codexClient struct{ proc *usageProc }

func (c codexClient) send(msg map[string]any) string {
	b, err := json.Marshal(msg)
	if err == nil {
		_, err = c.proc.stdin.Write(append(b, '\n'))
	}
	if err != nil {
		return c.proc.why("Codex", err, codexUsageTimeout)
	}
	return ""
}

// call sends method with params (none when nil) under id and decodes the
// result into out; it returns words when there was no result.
func (c codexClient) call(id int, method string, params any, out any) string {
	msg := map[string]any{"id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if w := c.send(msg); w != "" {
		return w
	}
	want := fmt.Sprint(id)
	for {
		line, err := c.proc.lines.next()
		if err != nil {
			return c.proc.why("Codex", err, codexUsageTimeout)
		}
		var r rpcReply
		if json.Unmarshal(line, &r) != nil {
			return "Codex sent a line that is not JSON-RPC"
		}
		if r.Method != "" || string(r.ID) != want {
			continue
		}
		if r.Error != nil {
			if r.Error.Code != nil {
				return fmt.Sprintf("Codex refused %s (error %d)", method, *r.Error.Code)
			}
			return "Codex refused " + method
		}
		if out != nil && json.Unmarshal(r.Result, out) != nil {
			return "Codex's answer to " + method + " is not in the shape Conductor reads"
		}
		return ""
	}
}

// codexReport maps the limits: each snapshot of rateLimitsByLimitId (or
// rateLimits when that is absent), in the order of its ids, as its
// windows and credits; the provider's flags go into Limits as given.
func codexReport(lim codexLimits) (UsageReport, error) {
	rep := UsageReport{State: UsageOK, Limits: UsageLimits{OrdinaryAllowed: lim.OrdinaryUsageAllowed}}
	snaps := lim.ByLimitID
	if len(snaps) == 0 {
		if lim.RateLimits == nil {
			return rep, fmt.Errorf("no rateLimits")
		}
		snaps = map[string]codexSnapshot{"": *lim.RateLimits}
	}
	ids := make([]string, 0, len(snaps))
	for id := range snaps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for i, id := range ids {
		s := snaps[id]
		// A field is named by its place, never by the provider's id.
		field := fmt.Sprintf("rateLimitsByLimitId[%d]", i)
		if id == "" {
			field = "rateLimits"
		}
		limit := id
		if s.LimitID != nil {
			limit = *s.LimitID
		}
		if limit != "" {
			var err error
			if limit, err = shownName(limit, usageMaxLimit, field+".limitId"); err != nil {
				return rep, err
			}
		}
		var limitName string
		if s.LimitName != nil && *s.LimitName != "" {
			var err error
			if limitName, err = shownName(*s.LimitName, usageMaxName/2, field+".limitName"); err != nil {
				return rep, err
			}
		}
		reached := s.RateLimitReachedType != nil && *s.RateLimitReachedType != ""
		if reached && !codexReached[*s.RateLimitReachedType] {
			return rep, fmt.Errorf("%s.rateLimitReachedType is not one Conductor knows", field)
		}
		if reached && rep.Limits.Reached == "" {
			rep.Limits.Reached = *s.RateLimitReachedType
		}
		if s.SpendControlReached != nil && (rep.Limits.SpendControl == nil || *s.SpendControlReached) {
			b := *s.SpendControlReached
			rep.Limits.SpendControl = &b
		}
		for _, k := range []struct {
			kind string
			w    *codexWindow
		}{{"primary", s.Primary}, {"secondary", s.Secondary}} {
			if k.w == nil {
				continue
			}
			w, err := codexWindowOf(k.kind, limit, limitName, k.w, reached)
			if err != nil {
				return rep, fmt.Errorf("%s.%s.%w", field, k.kind, err)
			}
			if len(rep.Windows) == usageMaxWindows {
				return rep, fmt.Errorf("more than %d windows", usageMaxWindows)
			}
			rep.Windows = append(rep.Windows, w)
		}
		if s.Credits != nil {
			bal, err := figure(s.Credits.Balance, field+".credits.balance")
			if err != nil {
				return rep, err
			}
			c := UsageCredit{Kind: "credits", Enabled: s.Credits.HasCredits, Balance: bal, Unlimited: s.Credits.Unlimited}
			if !hasCredit(rep.Credits, c) {
				if len(rep.Credits) == usageMaxCredits {
					return rep, fmt.Errorf("more than %d credit figures", usageMaxCredits)
				}
				rep.Credits = append(rep.Credits, c)
			}
		}
	}
	return rep, nil
}

func codexWindowOf(kind, limit, limitName string, cw *codexWindow, reached bool) (UsageWindow, error) {
	w := UsageWindow{Kind: kind, Limit: limit}
	if cw.UsedPercent == nil {
		return w, fmt.Errorf("usedPercent is missing")
	}
	var err error
	if w.Percent, err = percent(cw.UsedPercent.String(), "usedPercent"); err != nil {
		return w, err
	}
	if cw.WindowDurationMins != nil {
		if *cw.WindowDurationMins < 0 || *cw.WindowDurationMins > 1<<20 {
			return w, fmt.Errorf("windowDurationMins is out of range")
		}
		w.Minutes = int(*cw.WindowDurationMins)
	}
	switch m := w.Minutes; {
	case m == 300:
		w.Name = "5-hour"
	case m == 10080:
		w.Name = "Weekly"
	case m > 0 && m%60 == 0:
		w.Name = fmt.Sprintf("%d-hour", m/60)
	case m > 0:
		w.Name = fmt.Sprintf("%d-minute", m)
	case kind == "primary":
		w.Name = "Primary"
	default:
		w.Name = "Secondary"
	}
	if limitName != "" {
		w.Name = limitName + ", " + w.Name
	}
	if cw.ResetsAt != nil {
		if *cw.ResetsAt < 0 || *cw.ResetsAt > codexMaxReset {
			return w, fmt.Errorf("resetsAt is not a time Conductor reads")
		}
		t := time.Unix(*cw.ResetsAt, 0).UTC()
		w.ResetsAt = &t
	}
	if reached {
		w.Severity = "critical"
	}
	return w, nil
}

func hasCredit(cs []UsageCredit, c UsageCredit) bool {
	same := func(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	for _, x := range cs {
		if (x.Enabled == nil) == (c.Enabled == nil) && (x.Enabled == nil || *x.Enabled == *c.Enabled) && same(x.Balance, c.Balance) && x.Unlimited == c.Unlimited {
			return true
		}
	}
	return false
}

// codexPlan is the first snapshot's plan type, when account/read gave none.
func codexPlan(lim codexLimits) string {
	if lim.RateLimits != nil && lim.RateLimits.PlanType != nil && codexPlans[*lim.RateLimits.PlanType] {
		return *lim.RateLimits.PlanType
	}
	return ""
}
