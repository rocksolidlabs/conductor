package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"
)

// Claude Code's usage, read through its own commands: `auth status
// --json`, which decides how the login is paid, then for a subscription
// login of an admitted version the local /usage command in print mode,
// which answers with no model turn. Checked on 2.1.322, signed in, by the
// recipes tier's read (the result line's num_turns 0 and local_command
// "usage"), and signed out by its flag checks.
const (
	claudeUsageQualified = "2.1.322"
	// MinVersion is the checked version itself: the plan's reading on
	// 2.1.318 was not a check of this reader, and nobody checked the
	// versions between, so none of them is read.
	claudeUsageMinVersion = claudeUsageQualified
)

var (
	claudeAuthArgs = []string{"auth", "status", "--json"}
	// --setting-sources local: no hook or plugin of the person's runs;
	// --tools "": no tool can run whatever the version does, the bound
	// --help documents; --max-turns 1 bounds what a version that answers
	// with a turn spends. 2.1.322's --help no longer lists --max-turns,
	// and takes it. A version that refuses it ends the run before any
	// output (its parser's exit): the read is then error, "this version of
	// the tool is not one Conductor has checked", Inconclusive, with no
	// turn taken (claudeUsageRun); one that takes and ignores it still
	// runs no tool. The nightly tier cannot check it: --help hides it and
	// only a /usage run takes it, which needs a login and an admission; the
	// signed-in recipes read (TestRecipeClaudeUsageRead) does.
	claudeUsageArgs = []string{"-p", "/usage", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--setting-sources", "local", "--strict-mcp-config", "--tools", "", "--max-turns", "1"}

	// claudeProviders are the apiProvider values Conductor knows, with the
	// words for a provider that pays instead of a subscription; firstParty
	// is Anthropic's own. Any other value, or none, is not known.
	claudeProviders = map[string]string{"firstParty": "", "bedrock": "Amazon Bedrock", "vertex": "Google Vertex AI", "foundry": "Microsoft Foundry"}
	// claudeKeyMethods are the authMethod values that are a key.
	claudeKeyMethods = map[string]bool{"api_key": true}
	// claudeKeySources are the init line's apiKeySource values that are a
	// key; "none" is the subscription.
	claudeKeySources = map[string]bool{"ANTHROPIC_API_KEY": true, "apiKeyHelper": true, "/login managed key": true}
	// claudePlans are the subscription types reported; another is not.
	claudePlans = map[string]bool{"pro": true, "max": true, "team": true, "enterprise": true}
)

const claudeAuthMax = 64 << 10 // every byte of auth status's stdout

// currency is the shape of a currency code (ISO 4217).
var currency = regexp.MustCompile(`^[A-Z]{3}$`)

func claudeUsageReader() *UsageReader {
	return &UsageReader{Qualified: claudeUsageQualified, MinVersion: claudeUsageMinVersion, Read: readClaudeUsage}
}

// claudeAuth is what is kept of `auth status --json`: the email, the
// organisation and the login's expiry are never decoded.
type claudeAuth struct {
	LoggedIn         *bool   `json:"loggedIn"`
	AuthMethod       *string `json:"authMethod"`
	APIProvider      *string `json:"apiProvider"`
	SubscriptionType *string `json:"subscriptionType"`
}

// strOf is s, or "" for nil.
func strOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
	p, version, stop := qualify(ctx, in, claudeIdentity, "Claude Code", claudeUsageMinVersion, claudeUsageTimeout)
	if stop != nil {
		return *stop
	}
	rep.Version = version
	auth, words := claudeAuthStatus(ctx, p, in)
	provider, knownProvider := claudeProviders[strOf(auth.APIProvider)]
	method := strOf(auth.AuthMethod)
	switch {
	case words != "":
		return UsageReport{State: UsageError, Words: words, Version: version}
	case !*auth.LoggedIn:
		return UsageReport{State: UsageSignedOut, Words: "not signed in to Claude Code", Version: version}
	case strOf(auth.APIProvider) == "":
		return UsageReport{State: UsageError, Words: "Claude Code's auth status gives no apiProvider: how the login is paid is not known", Version: version}
	case !knownProvider:
		return UsageReport{State: UsageError, Words: "Claude Code's auth status names an apiProvider Conductor does not know: how the login is paid is not known", Version: version}
	case provider != "":
		return UsageReport{State: UsageKeyPaid, Words: "paid through " + provider + ": no usage windows are reported", Version: version}
	case claudeKeyMethods[method]:
		return UsageReport{State: UsageKeyPaid, Words: "signed in with an API key: no usage windows are reported", Version: version}
	case method == "":
		return UsageReport{State: UsageError, Words: "Claude Code's auth status gives no authMethod: how the login is paid is not known", Version: version}
	case method != "claude.ai":
		return UsageReport{State: UsageError, Words: "Claude Code's auth status names an authMethod Conductor does not know: how the login is paid is not known", Version: version}
	}
	var plan string
	if claudePlans[strOf(auth.SubscriptionType)] {
		plan = strOf(auth.SubscriptionType)
	}
	if !p.unchanged() {
		return UsageReport{State: UsageUnqualified, Words: "the program changed while it was checked", Version: version, Plan: plan}
	}
	// A read that has ended asks no admission: nothing would be started.
	if w := ended(ctx, "Claude Code", claudeUsageTimeout); w != "" {
		return UsageReport{State: UsageError, Words: w, Version: version, Plan: plan}
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
	// An admission granted is told exactly once, whatever came of it: a
	// read that ended inside the admission starts nothing (startUsage) and
	// tells it Inconclusive.
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
	proc, err := startUsage(ctx, p.with(claudeAuthArgs...), in, false, false)
	if err != nil {
		if w := ended(ctx, "Claude Code", claudeUsageTimeout); w != "" {
			return auth, w
		}
		return auth, "Claude Code's auth status could not be started"
	}
	defer proc.end()
	// claudeAuthMax: every byte of its stdout.
	out, err := io.ReadAll(io.LimitReader(proc.lines.r, claudeAuthMax+1))
	switch {
	case len(out) > claudeAuthMax:
		proc.stop()
		if w := ended(ctx, "Claude Code", claudeUsageTimeout); w != "" {
			return auth, w
		}
		return auth, "Claude Code's auth status printed more than 64 KiB"
	case err != nil, ctx.Err() != nil:
		return auth, proc.why("Claude Code's auth status", err, claudeUsageTimeout)
	}
	proc.end()
	if json.Unmarshal(out, &auth) != nil || auth.LoggedIn == nil {
		return auth, "Claude Code's auth status gave no answer Conductor reads" + proc.exitWords()
	}
	return auth, ""
}

// claudeUsageRun runs /usage and reads its lines until the result line,
// on past a line it cannot read (claudeStream), so that a result line that
// proves a turn is always seen. Its Outcome is claudeVerdict's.
func claudeUsageRun(ctx context.Context, p program, in UsageInput, version string) UsageReport {
	fail := func(words string) UsageReport { return UsageReport{State: UsageError, Words: words} }
	proc, err := startUsage(ctx, p.with(claudeUsageArgs...), in, false, false)
	if err != nil {
		if w := ended(ctx, "Claude Code", claudeUsageTimeout); w != "" {
			return fail(w)
		}
		return fail("Claude Code could not be started")
	}
	defer proc.end()
	var st claudeStream
	for seen := 0; ; seen++ {
		line, err := proc.lines.next()
		if err != nil {
			words := proc.why("Claude Code", err, claudeUsageTimeout)
			// Nothing printed and a failing exit is a version that refused
			// the read's arguments (claudeUsageArgs): no turn was taken.
			if seen == 0 && ctx.Err() == nil && proc.exitedFailing() {
				words = "Claude Code " + version + " ended before it printed anything" + proc.exitWords() + ": this version of the tool is not one Conductor has checked"
			}
			return fail(words)
		}
		if result := st.add(line); result != nil {
			proc.end()
			return claudeResult(result, st, version)
		}
	}
}

// jsonFields is a line's JSON object, each field decoded on its own, so a
// field Conductor does not read, or one that fails, hides no other.
type jsonFields map[string]json.RawMessage

// get decodes the named field into out. present: the field is there and
// not null; ok: it is absent, null, or of out's type.
func (f jsonFields) get(name string, out any) (present, ok bool) {
	raw, there := f[name]
	if !there || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, true
	}
	return true, json.Unmarshal(raw, out) == nil
}

// claudeStream is what the lines before the result line left: the init
// line's apiKeySource, the usage report's rate limits, and, in Conductor's
// words, the first line or part of one that could not be read (bad).
type claudeStream struct {
	keySource *string
	limits    *claudeRateLimits
	bad       string
}

func (s *claudeStream) note(words string) {
	if s.bad == "" {
		s.bad = words
	}
}

// add reads one line, never its message or local_command_source (the
// words the command prints for people) and never the usage report's
// session part; it returns the line's fields when it is the result line.
func (s *claudeStream) add(line []byte) jsonFields {
	var f jsonFields
	if json.Unmarshal(line, &f) != nil || f == nil {
		s.note("Claude Code printed a line that is not JSON")
		return nil
	}
	var typ, subtype string
	if _, ok := f.get("type", &typ); !ok {
		s.note("a line of Claude Code's names its type in a shape Conductor does not read")
		return nil
	}
	if typ == "result" {
		return f
	}
	if typ == "system" {
		if _, ok := f.get("subtype", &subtype); !ok {
			s.note("a system line of Claude Code's names its subtype in a shape Conductor does not read")
		}
	}
	if typ == "system" && subtype == "init" {
		var ks string
		if present, ok := f.get("apiKeySource", &ks); !ok {
			s.note("the init line's apiKeySource is not in the shape Conductor reads")
		} else if present {
			s.keySource = &ks
		}
	}
	var report jsonFields
	if present, ok := f.get("usage_report", &report); !ok {
		s.note("the usage report is not in the shape Conductor reads")
	} else if present {
		var rl claudeRateLimits
		if present, ok := report.get("rate_limits", &rl); !ok {
			s.note("the usage report's rate_limits are not in the shape Conductor reads")
		} else if present {
			s.limits = &rl
		}
	}
	return nil
}

// claudeVerdict is the one table from a /usage run's evidence to its
// outcome (the plan, PR-1: Local only for num_turns 0 and local_command
// "usage"; Turned for num_turns above 0 or another local_command;
// Inconclusive for every other end), read in the order of what each piece
// proves, so that the proof of a turn is never hidden by what only makes
// the run inconclusive. bad is the first line or part the stream could not
// read ("" when none); words say why a run is Inconclusive.
//
//  1. num_turns a whole number above 0                   Turned
//  2. local_command a string other than "usage"          Turned
//  3. a line before the result line, or a part, unread   Inconclusive
//  4. num_turns absent, null, or not a whole number >= 0 Inconclusive
//  5. local_command absent, null, or not a string        Inconclusive
//  6. is_error true or not a boolean, or subtype given
//     and not "success"                                  Inconclusive: the plan does not decide; fail closed
//  7. num_turns 0 and local_command "usage"              Local
//
// A run that ends without a result line (a timeout, its output's end or
// its exit, a bound passed) has no evidence and is Inconclusive too.
func claudeVerdict(f jsonFields, bad string) (Outcome, string) {
	var turns int
	turnsGiven, turnsOK := f.get("num_turns", &turns)
	turnsOK = turnsOK && turnsGiven && turns >= 0
	var command string
	commandGiven, commandOK := f.get("local_command", &command)
	commandOK = commandOK && commandGiven
	var isError bool
	_, errorOK := f.get("is_error", &isError)
	subtype := "success"
	_, subtypeOK := f.get("subtype", &subtype)
	switch {
	case turnsOK && turns > 0, commandOK && command != "usage":
		return Turned, ""
	case bad != "":
		return Inconclusive, bad
	case !turnsOK:
		return Inconclusive, "Claude Code's result line gave no turns"
	case !commandOK:
		return Inconclusive, "Claude Code's result line named no local command"
	case !errorOK || isError || !subtypeOK || subtype != "success":
		return Inconclusive, "Claude Code's result line says the /usage run failed"
	}
	return Local, ""
}

// claudeResult judges the run by claudeVerdict, then reads its report.
func claudeResult(f jsonFields, st claudeStream, version string) UsageReport {
	switch o, words := claudeVerdict(f, st.bad); o {
	case Turned:
		return UsageReport{State: UsageUnqualified, Outcome: Turned, Words: "Claude Code " + version + " answered /usage with a model turn: it is not run again"}
	case Inconclusive:
		return UsageReport{State: UsageError, Words: words}
	}
	rep := UsageReport{State: UsageOK, Outcome: Local}
	switch {
	case st.keySource == nil:
		rep.State, rep.Words = UsageError, "the /usage run did not say how it is paid"
	case claudeKeySources[*st.keySource]:
		rep.State, rep.Words = UsageKeyPaid, "the /usage run is paid by a key: no usage windows are reported"
	case *st.keySource != "none":
		rep.State, rep.Words = UsageError, "the /usage run names an apiKeySource Conductor does not know: how it is paid is not known"
	case st.limits == nil:
		rep.State, rep.Words = UsageError, "the /usage run reported no limits"
	default:
		var err error
		if rep.Windows, rep.Credits, err = claudeWindows(st.limits); err != nil {
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
				model, err := shownName(l.Scope.Model.DisplayName, usageMaxName-len("Weekly, "), field+".scope.model.display_name")
				if err != nil {
					return nil, nil, err
				}
				w.Name = "Weekly, " + model
			}
		default:
			return nil, nil, fmt.Errorf("%s.kind is not a window Conductor knows", field)
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
			if err != nil || !reportable(t) {
				return nil, nil, fmt.Errorf("%s.resets_at is not a time", field)
			}
			w.ResetsAt = &t
		}
		ws = append(ws, w)
	}
	var cs []UsageCredit
	if x := rl.ExtraUsage; x != nil {
		if x.Currency != "" && !currency.MatchString(x.Currency) {
			return nil, nil, fmt.Errorf("extra_usage.currency is not a currency code")
		}
		c := UsageCredit{Kind: "extra_usage", Enabled: x.IsEnabled, Currency: x.Currency}
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
