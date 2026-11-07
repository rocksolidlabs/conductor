package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"github.com/rocksolidlabs/conductor/internal/session"
	"github.com/rocksolidlabs/conductor/internal/store"
)

// Webhook is a URL the server POSTs activity entries to: the entries of every
// session whose event type Events lists (ValidWebhookEvent). With a Secret,
// each request carries an HMAC-SHA256 signature of its body. The URL may not
// reach a loopback, link-local, private, shared or unspecified address unless
// AllowPrivate says so (CheckWebhookAddr).
type Webhook struct {
	URL          string   `json:"url"`
	Events       []string `json:"events"`
	Secret       string   `json:"secret"`
	AllowPrivate bool     `json:"allowPrivate"`
}

// Bounds for webhooks.
const (
	MaxWebhooks   = 16
	MaxWebhookURL = 2048 // bytes
)

// EventExitNonZero is the event type of the status entry of a process that
// exited on its own with a non-zero code (session.ExitCode), as the Events
// page names it.
const EventExitNonZero = "exit_nonzero"

// EventDecision is the event of a durable decision's change (opened,
// answered, applied, superseded, cancelled, commented), which the server
// sends after each commit (docs/plans/decisions.md, Notifications).
const EventDecision = "decision"

// webhookResolveTimeout bounds the lookups of the webhooks' hosts at
// validation, which run at once.
const webhookResolveTimeout = 2 * time.Second

// ValidWebhookEvent reports whether a webhook may list t: an activity entry
// type (session.ValidEventType); an attention state, needs_input, working or
// done, which an attention entry counts as when it records it;
// EventExitNonZero; or EventDecision. These are the Events page's names.
func ValidWebhookEvent(t string) bool {
	switch session.AttentionState(t) {
	case session.AttentionNeedsInput, session.AttentionWorking, session.AttentionDone:
		return true
	}
	return t == EventExitNonZero || t == EventDecision || session.ValidEventType(t)
}

// Endpoint is the webhook's URL without its user info, query and fragment,
// any of which may hold a token: scheme://host[:port]/path, what the API
// shows of it. The path is kept, so a service that puts its credential there
// (Slack, Discord) shows it to admins. It is "" for a URL that does not parse.
func (w Webhook) Endpoint() string {
	return ShownURL(w.URL)
}

// check checks one webhook without looking anything up, and returns the host
// of its URL. No message quotes the URL: its query or user info may hold a
// token.
func (w Webhook) check() (string, error) {
	switch {
	case w.URL == "":
		return "", errors.New("url must not be empty")
	case len(w.URL) > MaxWebhookURL:
		return "", fmt.Errorf("url must be at most %d bytes", MaxWebhookURL)
	}
	u, err := url.Parse(w.URL)
	if err != nil {
		return "", errors.New("url is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("url must be http or https")
	}
	if u.Opaque != "" || u.Hostname() == "" {
		return "", errors.New("url must name a host")
	}
	if len(w.Events) == 0 {
		return "", errors.New("events must list at least one event type")
	}
	for _, t := range w.Events {
		if !ValidWebhookEvent(t) {
			return "", fmt.Errorf("events: %q is not an event type", t)
		}
	}
	return u.Hostname(), nil
}

// validateWebhooks checks the webhooks. The host of each one that does not
// allow private addresses is checked with CheckWebhookAddr: an address in the
// URL at once, a name after a lookup. The lookups run at once, for at most
// webhookResolveTimeout: a host that resolves to an address the rule refuses
// is an error, and one that does not resolve (DNS may be down while the
// server starts) a warning, for the host is checked again before every
// connection. It returns the errors and the warnings.
func (c *Config) validateWebhooks() (errs []error, warnings []string) {
	if len(c.Webhooks) > MaxWebhooks {
		// Past the limit none is checked: each check may look a host up.
		return []error{fmt.Errorf("webhooks: at most %d, got %d", MaxWebhooks, len(c.Webhooks))}, nil
	}
	type lookup struct {
		index int
		host  string
		addrs []netip.Addr
		err   error
	}
	var lookups []*lookup
	for i, w := range c.Webhooks {
		host, err := w.check()
		if err != nil {
			errs = append(errs, fmt.Errorf("webhooks[%d]: %w", i, err))
			continue
		}
		if w.AllowPrivate {
			continue
		}
		if a, err := netip.ParseAddr(host); err == nil {
			if err := CheckWebhookAddr(a); err != nil {
				errs = append(errs, fmt.Errorf("webhooks[%d]: %w", i, err))
			}
			continue
		}
		lookups = append(lookups, &lookup{index: i, host: host})
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookResolveTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, l := range lookups {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.addrs, l.err = LookupWebhookHost(ctx, l.host)
		}()
	}
	wg.Wait()
	for _, l := range lookups {
		if l.err == nil && len(l.addrs) == 0 {
			l.err = errors.New("no address")
		}
		if l.err != nil {
			warnings = append(warnings, fmt.Sprintf("webhooks[%d]: %s did not resolve (%v); it is checked again before every delivery", l.index, l.host, l.err))
			continue
		}
		for _, a := range l.addrs {
			if err := CheckWebhookAddr(a); err != nil {
				errs = append(errs, fmt.Errorf("webhooks[%d]: %s: %w", l.index, l.host, err))
				break
			}
		}
	}
	return errs, warnings
}

// LookupWebhookHost resolves the host name of a webhook at validation. Tests
// replace it.
var LookupWebhookHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Ranges of CheckWebhookAddr beside those netip names.
var (
	sharedAddressSpace = netip.MustParsePrefix("100.64.0.0/10")   // RFC 6598, CGNAT; 100.100.100.200 is a cloud metadata address
	ipv4Compatible     = netip.MustParsePrefix("::/96")           // ::a.b.c.d, deprecated
	nat64WellKnown     = netip.MustParsePrefix("64:ff9b::/96")    // RFC 6052
	nat64LocalUse      = netip.MustParsePrefix("64:ff9b:1::/48")  // RFC 8215
	sixToFour          = netip.MustParsePrefix("2002::/16")       // RFC 3056
	teredo             = netip.MustParsePrefix("2001::/32")       // RFC 4380
	siit               = netip.MustParsePrefix("::ffff:0:0:0/96") // RFC 2765, IPv4-translated
)

// CheckWebhookAddr is the network rule for one address a webhook would
// connect to. Unless the webhook allows private addresses, a loopback,
// link-local, private or unique-local, shared (CGNAT) or unspecified address,
// IPv4 or IPv6, is refused, and so is an IPv4-compatible IPv6 address, so that
// a webhook reaches no service of the server's machine or its network that is
// not published. An IPv4 address written as IPv6 counts as the IPv4 one. An
// IPv6 address that carries IPv4 addresses for a transition mechanism (NAT64,
// 6to4, Teredo, SIIT) counts as each one it carries. An address of
// 64:ff9b:1::/48 is read as a /96 NAT64 prefix; one in the form that a shorter
// prefix gives (u octet zero and the last three bytes zero), or with a u octet
// that is not zero, which no NAT64 prefix gives, is refused. The server applies it to every address of a webhook's host when it
// validates its config, and to every address it is about to connect to,
// looked up again for each connection, so a name that resolves elsewhere by
// then (DNS rebinding) reaches no such address either.
func CheckWebhookAddr(a netip.Addr) error {
	u := a.Unmap().WithZone("")
	if u.Is6() {
		via, v4s, err := embeddedIPv4(u)
		if err != nil {
			return fmt.Errorf("%s %w; set allowPrivate to send to it", a, err)
		}
		for _, v4 := range v4s {
			if what := addrRange(v4); what != "" {
				return fmt.Errorf("%s reaches %s through %s, which is %s; set allowPrivate to send to it", a, v4, via, what)
			}
		}
		if via != "" {
			return nil
		}
	}
	if what := addrRange(u); what != "" {
		return fmt.Errorf("%s is %s; set allowPrivate to send to it", a, what)
	}
	return nil
}

// Refusals of an address of 64:ff9b:1::/48, which is read as a /96 NAT64
// prefix otherwise. errShorterNAT64 is the form a prefix shorter than /96 may
// have made: RFC 6052 puts the IPv4 address elsewhere for those, leaving bits
// 64 to 71 (the u octet) zero and the bytes after the IPv4 address zero.
// errNAT64UOctet is an address whose u octet is not zero, which no RFC 6052
// form has, so it may be anything and no IPv4 address can be read from it.
var (
	errShorterNAT64 = errors.New("is in 64:ff9b:1::/48 in the form a NAT64 prefix shorter than /96 gives, so the IPv4 address it reaches cannot be told (only /96 prefixes are judged)")
	errNAT64UOctet  = errors.New("is not a NAT64 address: it is in 64:ff9b:1::/48 but its u octet (bits 64 to 71) is not zero, which no NAT64 prefix gives")
)

// embeddedIPv4 returns the IPv4 addresses that a, an IPv6 address without a
// zone, reaches through a transition mechanism, and that mechanism's name:
// NAT64 (64:ff9b::/96, or a /96 in 64:ff9b:1::/48), 6to4 (2002::/16), Teredo
// (2001::/32, its server and its client) or SIIT (::ffff:0:a.b.c.d). via is ""
// for an address that embeds none, and err refuses an address of
// 64:ff9b:1::/48 that is not read as a /96 (errShorterNAT64, errNAT64UOctet).
func embeddedIPv4(a netip.Addr) (via string, v4 []netip.Addr, err error) {
	b := a.As16()
	at := func(i int) netip.Addr { return netip.AddrFrom4([4]byte(b[i : i+4])) }
	switch {
	case nat64LocalUse.Contains(a):
		// Read as a /96 unless it is in a form that a shorter prefix gives (u
		// octet zero, the last three bytes zero), or has a u octet that no
		// prefix gives: both are refused.
		switch {
		case b[8] != 0:
			return "", nil, errNAT64UOctet
		case b[13] == 0 && b[14] == 0 && b[15] == 0:
			return "", nil, errShorterNAT64
		}
		return "NAT64", []netip.Addr{at(12)}, nil
	case nat64WellKnown.Contains(a):
		return "NAT64", []netip.Addr{at(12)}, nil
	case sixToFour.Contains(a):
		return "6to4", []netip.Addr{at(2)}, nil
	case teredo.Contains(a):
		client := netip.AddrFrom4([4]byte{^b[12], ^b[13], ^b[14], ^b[15]})
		return "Teredo", []netip.Addr{at(4), client}, nil
	case siit.Contains(a):
		return "SIIT", []netip.Addr{at(12)}, nil
	}
	return "", nil, nil
}

// addrRange names the range of a, unmapped and without a zone, that a webhook
// may reach only with allowPrivate, or is "" for an address outside them all.
func addrRange(a netip.Addr) string {
	switch {
	case a.IsLoopback():
		return "a loopback address"
	case a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
		return "a link-local address"
	case a.IsPrivate() && a.Is4():
		return "a private address"
	case a.IsPrivate():
		return "a unique-local address"
	case a.IsUnspecified(), a.Is4() && a.As4()[0] == 0:
		// 0.0.0.0/8 is "this network": a connection to it reaches this host.
		return "an unspecified address"
	case sharedAddressSpace.Contains(a):
		return "in the shared address space (CGNAT)"
	case a.Is6() && ipv4Compatible.Contains(a):
		return "an IPv4-compatible address (deprecated)"
	}
	return ""
}

// parseWebhooks reads the value of CONDUCTOR_WEBHOOKS: a JSON array of
// webhooks, as the config file holds them, and nothing after it
// (store.DecodeStrict, as for the config file itself).
func parseWebhooks(v string) ([]Webhook, error) {
	var hooks []Webhook
	if err := store.DecodeStrict([]byte(v), &hooks); err != nil {
		return nil, err
	}
	return hooks, nil
}
