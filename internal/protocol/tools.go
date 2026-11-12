package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// ClientToolPrefix marks every client tool the gateway renames on a shaped
// free-tier request. The prefix keeps client tools distinct from the official
// OpenCode set the gateway appends, and lets responses be restored to the names
// the client declared. What follows it is a digest, never the client name: the
// free tier refuses bodies that still read as another client's session.
const ClientToolPrefix = "clint_"

// clientToolDigestLength is how many hex characters of the client name digest
// a wire name carries. Twelve is far past the collision point for the handful
// of tools one request declares.
const clientToolDigestLength = 12

// toolWireNameLimit is the longest tool name the upstreams accept.
const toolWireNameLimit = 64

// ToolCallFilter selects which upstream tool calls the gateway withholds from
// the client because the client cannot execute them.
type ToolCallFilter int

const (
	// FilterOff keeps every call (the historical behavior).
	FilterOff ToolCallFilter = iota
	// FilterManifestOnly drops an official/manifest tool call when the caller
	// declared no equivalent. Names the model invented are kept.
	FilterManifestOnly
	// FilterUnoffered drops any call the caller did not declare, including
	// names the model invented.
	FilterUnoffered
)

// ParseToolCallFilter maps the configuration spelling to a filter. Unknown
// values fall back to off so a typo cannot silently change response bodies.
func ParseToolCallFilter(value string) ToolCallFilter {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manifest-only", "manifest_only", "manifest":
		return FilterManifestOnly
	case "unoffered", "strict":
		return FilterUnoffered
	default:
		return FilterOff
	}
}

// ToolRenames records the client-to-wire tool names of one shaped request. The
// zero value and a nil pointer are both empty and safe to use.
type ToolRenames struct {
	toWire   map[string]string
	toClient map[string]string
	official map[string]string
	// byCase and byShape map a folded client name back to the one declared
	// client tool it names. A value of "" marks a fold two tools share: that
	// name is never guessed between.
	byCase  map[string]string
	byShape map[string]string
	// manifest is every official name appended for the lane, and declared is
	// every client tool the callers declared. filter selects the reply-side
	// policy that uses them.
	manifest map[string]bool
	declared map[string]bool
	filter   ToolCallFilter
}

// NewToolRenames returns an empty rename set.
func NewToolRenames() *ToolRenames {
	return &ToolRenames{
		toWire:   map[string]string{},
		toClient: map[string]string{},
		official: map[string]string{},
		byCase:   map[string]string{},
		byShape:  map[string]string{},
	}
}

// Empty reports whether no tool was renamed.
func (r *ToolRenames) Empty() bool {
	return r == nil || len(r.toWire) == 0
}

// Active reports whether the response side must touch this body: either a
// client tool was renamed or a reply-side filter is enabled. Unlike Empty it
// stays true for a shaped request whose caller declared no tools, because the
// manifest filter must still drop unrunnable official calls.
func (r *ToolRenames) Active() bool {
	return r != nil && (len(r.toWire) > 0 || r.filter != FilterOff)
}

// HasFilter reports whether reply-side tool-call filtering is enabled.
func (r *ToolRenames) HasFilter() bool {
	return r != nil && r.filter != FilterOff
}

// SetManifest records the official tool names appended for this lane.
func (r *ToolRenames) SetManifest(names []string) {
	if r == nil {
		return
	}
	r.manifest = make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			r.manifest[name] = true
		}
	}
}

// SetDeclared records the client tool names the caller declared.
func (r *ToolRenames) SetDeclared(names []string) {
	if r == nil {
		return
	}
	r.declared = make(map[string]bool, len(names))
	for _, name := range names {
		if name != "" {
			r.declared[name] = true
		}
	}
}

// SetFilter enables reply-side tool-call filtering.
func (r *ToolRenames) SetFilter(filter ToolCallFilter) {
	if r == nil {
		return
	}
	r.filter = filter
}

// ShouldDrop reports whether a tool name the upstream returned cannot be
// executed by the caller under the configured filter. A renamed client tool, a
// bound official alias, or a declared client name is always kept.
func (r *ToolRenames) ShouldDrop(name string) bool {
	if r == nil || r.filter == FilterOff || name == "" {
		return false
	}
	if _, ok := r.Client(name); ok {
		return false
	}
	if _, ok := r.Official(name); ok {
		return false
	}
	if r.declared[name] {
		return false
	}
	if resolved, _ := r.Resolve(name); resolved != name {
		return false
	}
	if r.manifest[name] {
		return true
	}
	return r.filter == FilterUnoffered
}

// ManifestHasPrefix reports whether any official name starts with prefix and is
// longer. A Chat stream uses it to hold a partial name back until the next
// chunk resolves it, so a manifest-only drop never leaks a name fragment.
func (r *ToolRenames) ManifestHasPrefix(prefix string) bool {
	if r == nil || prefix == "" || !r.HasFilter() {
		return false
	}
	for name := range r.manifest {
		if len(name) > len(prefix) && strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Count reports how many client tools were renamed.
func (r *ToolRenames) Count() int {
	if r == nil {
		return 0
	}
	return len(r.toWire)
}

// Wire returns the wire name registered for an exact client name.
func (r *ToolRenames) Wire(client string) (string, bool) {
	if r == nil || client == "" {
		return "", false
	}
	wire, ok := r.toWire[client]
	return wire, ok
}

// Client returns the client name registered for an exact wire name.
func (r *ToolRenames) Client(wire string) (string, bool) {
	if r == nil || wire == "" {
		return "", false
	}
	client, ok := r.toClient[wire]
	return client, ok
}

// BindOfficial records the declared client tool that matches one official name.
func (r *ToolRenames) BindOfficial(official, client string) {
	if r == nil || official == "" || client == "" {
		return
	}
	if r.official == nil {
		r.official = map[string]string{}
	}
	if _, exists := r.official[official]; !exists {
		r.official[official] = client
	}
}

// Official returns the declared client tool bound to an official name.
func (r *ToolRenames) Official(official string) (string, bool) {
	if r == nil || official == "" {
		return "", false
	}
	client, ok := r.official[official]
	return client, ok
}

// Add records one client-to-wire mapping. Adding the same pair twice is a
// no-op; a conflicting pair keeps the first wire name so the map stays
// invertible.
func (r *ToolRenames) Add(client, wire string) {
	if r == nil || client == "" || wire == "" || client == wire {
		return
	}
	if existing, ok := r.toWire[client]; ok {
		if existing != wire {
			return
		}
	}
	if _, taken := r.toClient[wire]; taken {
		return
	}
	r.toWire[client] = wire
	r.toClient[wire] = client
	r.bindFolded(r.byCase, strings.ToLower(client), client)
	r.bindFolded(r.byShape, foldToolName(client), client)
}

// bindFolded records client under key in a folded map. An empty key names
// nothing (a client tool spelled only with separators must not be handed every
// other such name), and a key two clients share is marked ambiguous.
func (r *ToolRenames) bindFolded(folded map[string]string, key, client string) {
	if folded == nil || key == "" {
		return
	}
	if existing, ok := folded[key]; ok {
		if existing != client {
			folded[key] = ""
		}
		return
	}
	folded[key] = client
}

// toolNameSeparators are the characters ignored when matching a tool name by
// shape, after case: "todo_write", "todo-write", "todo.write" and "todowrite"
// all fold to one key.
const toolNameSeparators = "_.- \t\r\n"

// foldToolName lowercases name and removes the separators agents spell tool
// names with, so two spellings of one tool fold together.
func foldToolName(name string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(name) {
		if !strings.ContainsRune(toolNameSeparators, char) {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// HasPrefix reports whether any wire name starts with prefix.
func (r *ToolRenames) HasPrefix(prefix string) bool {
	if r == nil || prefix == "" {
		return false
	}
	for wire := range r.toClient {
		if strings.HasPrefix(wire, prefix) {
			return true
		}
	}
	return false
}

// HasLongerPrefix reports whether any wire name is longer than prefix and
// starts with it. A stream fragment builder uses this to hold a partial name
// back until the next chunk resolves it.
func (r *ToolRenames) HasLongerPrefix(prefix string) bool {
	if r == nil || prefix == "" {
		return false
	}
	for wire := range r.toClient {
		if len(wire) > len(prefix) && strings.HasPrefix(wire, prefix) {
			return true
		}
	}
	return false
}

// Resolve maps a tool name the upstream reported back to the name the client
// declared. It is the response-side counterpart of the wire rename: an exact
// wire name restores, an official alias maps to the matching client tool, a
// clint_ prefix is stripped as a best-effort recovery (for example after a
// configuration reload mid-stream), and a name that
// differs from exactly one declared client tool only in case, or else only in
// case and separators (todowrite → todo_write), is folded onto that tool. A
// name two tools fold to is never guessed between. It reports whether the
// result differs from the name given.
func (r *ToolRenames) Resolve(wire string) (string, bool) {
	if r == nil || wire == "" {
		return wire, false
	}
	if client, ok := r.Client(wire); ok {
		return client, client != wire
	}
	if client, ok := r.Official(wire); ok {
		return client, client != wire
	}
	client := wire
	if rest, ok := strings.CutPrefix(wire, ClientToolPrefix); ok && rest != "" {
		client = rest
	}
	if _, declared := r.toWire[client]; declared {
		return client, client != wire
	}
	lower := strings.ToLower(client)
	if match, ok := r.byCase[lower]; ok {
		// A key present with an empty value is two client tools folded
		// together; the name as written is the only safe answer.
		if match != "" {
			return match, match != wire
		}
		return client, client != wire
	}
	if match, ok := r.byShape[foldToolName(client)]; ok && match != "" {
		return match, match != wire
	}
	return client, client != wire
}

// Restore maps a wire name back to the client name. See Resolve for the rules;
// a name it cannot map is returned as is.
func (r *ToolRenames) Restore(wire string) string {
	client, _ := r.Resolve(wire)
	return client
}

// ClientToolWireName builds the wire name for one client tool. The name is
// deliberately opaque: the free tier refuses a shaped body that still carries
// another client's vocabulary (Claude Code's ReportFindings and ScheduleWakeup
// together are enough, and dropping either name lifts the refusal), so no
// client tool name may reach the wire. The digest keeps one client tool on one
// wire name across requests, and collisions with used names get a _2, _3, …
// suffix. The chosen name is recorded in used.
func ClientToolWireName(name string, used map[string]bool) string {
	if used == nil {
		used = map[string]bool{}
	}
	digest := sha256.Sum256([]byte(name))
	clean := hex.EncodeToString(digest[:])[:clientToolDigestLength]
	max := toolWireNameLimit - len(ClientToolPrefix)
	if len(clean) > max {
		clean = clean[:max]
	}
	base := ClientToolPrefix + clean
	if !used[base] {
		used[base] = true
		return base
	}
	for suffix := 2; ; suffix++ {
		tag := "_" + strconv.Itoa(suffix)
		cut := toolWireNameLimit - len(tag)
		if cut > len(base) {
			cut = len(base)
		}
		candidate := base[:cut] + tag
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}
