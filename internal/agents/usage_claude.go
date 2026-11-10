package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Claude Code's usage, read through its own commands: `auth status
// --json`, which decides how the login is paid, then for a subscription
// login of an admitted version the local /usage command in print mode,
// which answers with no model turn. Checked by hand on 2.1.322 (the result
// line's num_turns 0 and local_command "usage") and, signed out, by the
// recipes tier.
const (
	claudeUsageQualified = "2.1.322"
	// The plan's reading on 2.1.318 answered the same way, with the same
	// fields.
	claudeUsageMinVersion = "2.1.318"
)

var (
	claudeAuthArgs = []string{"auth", "status", "--json"}
	// --setting-sources local: no hook or plugin of the person's runs;
	// --tools "": no tool can run whatever the version does; --max-turns
	// 1 bounds what a version that answers with a turn spends (a flag
	// 2.1.322's --help no longer lists, and still takes).
	claudeUsageArgs = []string{"-p", "/usage", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--setting-sources", "local", "--strict-mcp-config", "--tools", "", "--max-turns", "1"}
)

const claudeAuthMax = 64 << 10

func claudeUsageReader() *UsageReader {
	return &UsageReader{Qualified: claudeUsageQualified, MinVersion: claudeUsageMinVersion, Read: readClaudeUsage}
}

// claudeAuth is what is kept of `auth status --json`: the email, the
// organisation and the login's expiry are never decoded.
type claudeAuth struct {
	LoggedIn         *bool  `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	SubscriptionType string `json:"subscriptionType"`
}

// claudeLine is what is read of one stream-json line: the init line's
// apiKeySource, the usage report's rate limits (never its session part,
// never the message or local_command_source, the words the command prints
// for people), and the result line's turns and command.
type claudeLine struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	APIKeySource *string `json:"apiKeySource"`
	UsageReport  *struct {
		RateLimits *claudeRateLimits `json:"rate_limits"`
	} `json:"usage_report"`
	NumTurns     *int    `json:"num_turns"`
	LocalCommand *string `json:"local_command"`
}

type claudeRateLimits struct {
	Limits []struct {
		Kind     string       `json:"kind"`
		Percent  *json.Number `json:"percent"`
		ResetsAt *string      `json:"resets_at"`
		Scope    *struct {
			Model *struct {
				DisplayName string `json:"display_name"`
			} `json:"model"`
		} `json:"scope"`
		Severity string `json:"severity"`
		IsActive *bool  `json:"is_active"`
	} `json:"limits"`
	ExtraUsage *struct {
		IsEnabled    *bool        `json:"is_enabled"`
		MonthlyLimit *json.Number `json:"monthly_limit"`
		UsedCredits  *json.Number `json:"used_credits"`
		Currency     string       `json:"currency"`
	} `json:"extra_usage"`
}

func readClaudeUsage(ctx context.Context, in UsageInput) (rep UsageReport) {
	defer func() { rep.ReadAt = time.Now(); rep.Words = clip(rep.Words, usageMaxWords) }()
	ctx, cancel := withUsageTimeout(ctx, claudeUsageTimeout)
	defer cancel()
	p, version, stop := qualify(ctx, in, claudeIdentity, "Claude Code", claudeUsageMinVersion)
	if stop != nil {
		return *stop
	}
	rep.Version = version
	auth, words := claudeAuthStatus(ctx, p, in)
	switch {
	case words != "":
		return UsageReport{State: UsageError, Words: words, Version: version}
	case !*auth.LoggedIn:
		return UsageReport{State: UsageSignedOut, Words: "not signed in to Claude Code", Version: version}
	case auth.APIProvider != "firstParty":
		return UsageReport{State: UsageKeyPaid, Words: "paid through " + clip(auth.APIProvider, usageMaxLimit) + ": no usage windows are reported", Version: version}
	case strings.Contains(strings.ToLower(auth.AuthMethod), "key"):
		return UsageReport{State: UsageKeyPaid, Words: "signed in with an API key: no usage windows are reported", Version: version}
	case auth.AuthMethod != "claude.ai":
		return UsageReport{State: UsageError, Words: "auth status names a sign-in Conductor does not read: " + clip(auth.AuthMethod, usageMaxLimit), Version: version}
	}
	plan := clip(auth.SubscriptionType, usageMaxPlan)
	if !p.unchanged() {
		return UsageReport{State: UsageUnqualified, Words: "the program changed while it was checked", Version: version, Plan: plan}
	}
	newer := versionLess(claudeUsageQualified, version)
	if in.Admit == nil {
		w := "no run of Claude Code " + version + " was admitted"
		if newer {
			w = version + " is newer than the " + claudeUsageQualified + " the reader was checked on"
		}
		return UsageReport{State: UsageUnqualified, Words: w, Version: version, Plan: plan}
	}
	adm, err := in.Admit(version)
	if err != nil || adm == nil {
		w := "no run of Claude Code " + version + " was admitted"
		if err != nil {
			w = err.Error()
		}
		return UsageReport{State: UsageUnqualified, Words: w, Version: version, Plan: plan}
	}
	rep = claudeUsageRun(ctx, p, in, version)
	adm.Done(rep.Outcome)
	rep.Version = version
	if rep.State == UsageOK || rep.State == UsageKeyPaid {
		rep.Plan = plan
	}
	return rep
}

// claudeAuthStatus runs `auth status --json` and decodes its four fields;
// words say why when it gave no answer read.
func claudeAuthStatus(ctx context.Context, p program, in UsageInput) (claudeAuth, string) {
	var auth claudeAuth
	proc, err := startUsage(ctx, p.with(claudeAuthArgs...), in, false)
	if err != nil {
		return auth, clip("Claude Code's auth status could not start: "+err.Error(), usageMaxWords)
	}
	defer proc.end()
	out, err := io.ReadAll(io.LimitReader(proc.lines.r, claudeAuthMax+1))
	switch {
	case err != nil, ctx.Err() != nil:
		return auth, proc.why("Claude Code's auth status", err, claudeUsageTimeout)
	case len(out) > claudeAuthMax:
		return auth, "Claude Code's auth status printed more than 64 KiB"
	}
	if json.Unmarshal(out, &auth) != nil || auth.LoggedIn == nil {
		proc.end()
		w := "Claude Code's auth status gave no answer Conductor reads"
		if line := firstLine(proc.stderr.String()); line != "" {
			w += ": " + line
		}
		return auth, clip(w, usageMaxWords)
	}
	return auth, ""
}

// claudeUsageRun runs /usage and reads its lines until the result line.
// Its Outcome is what that line proved: Local only for num_turns 0 and
// local_command "usage".
func claudeUsageRun(ctx context.Context, p program, in UsageInput, version string) UsageReport {
	fail := func(words string) UsageReport { return UsageReport{State: UsageError, Words: words} }
	proc, err := startUsage(ctx, p.with(claudeUsageArgs...), in, false)
	if err != nil {
		return fail("Claude Code could not start: " + err.Error())
	}
	defer proc.end()
	var keySource *string
	var limits *claudeRateLimits
	for {
		line, err := proc.lines.next()
		if err != nil {
			return fail(proc.why("Claude Code", err, claudeUsageTimeout))
		}
		var l claudeLine
		if json.Unmarshal(line, &l) != nil {
			proc.end()
			return fail("Claude Code printed a line that is not JSON")
		}
		switch {
		case l.Type == "system" && l.Subtype == "init":
			keySource = l.APIKeySource
		case l.UsageReport != nil && l.UsageReport.RateLimits != nil:
			limits = l.UsageReport.RateLimits
		case l.Type == "result":
			proc.end()
			return claudeResult(l, keySource, limits, version)
		}
	}
}

// claudeResult judges the run by its result line, then reads its report.
func claudeResult(l claudeLine, keySource *string, limits *claudeRateLimits, version string) UsageReport {
	switch {
	case l.NumTurns == nil || *l.NumTurns < 0:
		return UsageReport{State: UsageError, Words: "Claude Code's result line gave no turns"}
	case *l.NumTurns > 0 || (l.LocalCommand != nil && *l.LocalCommand != "usage"):
		return UsageReport{State: UsageUnqualified, Outcome: Turned, Words: "Claude Code " + version + " answered /usage with a model turn: it is not run again"}
	case l.LocalCommand == nil:
		return UsageReport{State: UsageError, Words: "Claude Code's result line named no local command"}
	}
	rep := UsageReport{State: UsageOK, Outcome: Local}
	switch {
	case keySource == nil:
		rep.State, rep.Words = UsageError, "the /usage run did not say how it is paid"
	case *keySource != "none":
		rep.State, rep.Words = UsageKeyPaid, "paid by "+clip(*keySource, usageMaxLimit)+": no usage windows are reported"
	case limits == nil:
		rep.State, rep.Words = UsageError, "the /usage run reported no limits"
	default:
		var err error
		if rep.Windows, rep.Credits, err = claudeWindows(limits); err != nil {
			rep.State, rep.Words, rep.Windows, rep.Credits = UsageError, "Claude Code's usage report: "+err.Error(), nil, nil
		}
	}
	return rep
}

// claudeWindows maps the report's limits; a kind or severity it does not
// know fails the read rather than being guessed at.
func claudeWindows(rl *claudeRateLimits) ([]UsageWindow, []UsageCredit, error) {
	if len(rl.Limits) > usageMaxWindows {
		return nil, nil, fmt.Errorf("more than %d windows", usageMaxWindows)
	}
	var ws []UsageWindow
	for i, l := range rl.Limits {
		field := fmt.Sprintf("limits[%d]", i)
		w := UsageWindow{Kind: l.Kind, Active: l.IsActive}
		switch l.Kind {
		case "session":
			w.Name = "5-hour"
		case "weekly_all":
			w.Name = "Weekly, all models"
		case "weekly_scoped":
			w.Name = "Weekly, one model"
			if l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "" {
				w.Name = clip("Weekly, "+l.Scope.Model.DisplayName, usageMaxName)
			}
		default:
			return nil, nil, fmt.Errorf("%s.kind %q is not a window Conductor knows", field, clip(l.Kind, usageMaxLimit))
		}
		switch l.Severity {
		case "", "normal", "warning", "critical":
			w.Severity = l.Severity
		default:
			return nil, nil, fmt.Errorf("%s.severity is not one Conductor knows", field)
		}
		if l.Percent == nil {
			return nil, nil, fmt.Errorf("%s.percent is missing", field)
		}
		var err error
		if w.Percent, err = percent(l.Percent.String(), field+".percent"); err != nil {
			return nil, nil, err
		}
		if l.ResetsAt != nil {
			t, err := time.Parse(time.RFC3339Nano, *l.ResetsAt)
			if err != nil {
				return nil, nil, fmt.Errorf("%s.resets_at is not a time", field)
			}
			w.ResetsAt = &t
		}
		ws = append(ws, w)
	}
	var cs []UsageCredit
	if x := rl.ExtraUsage; x != nil {
		c := UsageCredit{Kind: "extra_usage", Enabled: x.IsEnabled, Currency: clip(x.Currency, usageMaxCurrency)}
		var err error
		if c.Used, err = figure((*string)(x.UsedCredits), "extra_usage.used_credits"); err != nil {
			return nil, nil, err
		}
		if c.Limit, err = figure((*string)(x.MonthlyLimit), "extra_usage.monthly_limit"); err != nil {
			return nil, nil, err
		}
		cs = append(cs, c)
	}
	return ws, cs, nil
}
