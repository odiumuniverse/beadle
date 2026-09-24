package agent

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/odiumuniverse/beadle/pkg/config"
	"github.com/odiumuniverse/beadle/pkg/kind"
	"github.com/odiumuniverse/beadle/pkg/mcp"
)

const (
	// dshPatchFile is the home-level user patch layer DSH applies over every
	// profile's own layer (upstream `homePatchPath`; verified live on
	// 0.1.5-rc.3 for A-44). DSH never creates the file itself.
	dshPatchFile = "cordis.patch.yml"

	// dshMCPPlugin is the plugin a DSH MCP record must name.
	dshMCPPlugin = "@deepseek-ai/dsh-mcp-client"

	// dshMCPIDPrefix namespaces beadle's records inside the patch list. An id
	// is a patch row key, not a server name: the canon name is the record's
	// config.serverName, the name DSH mounts.
	dshMCPIDPrefix = "beadle:"

	// dshMCPStdio and dshMCPHTTP are the transports the plugin config accepts.
	// DSH has no SSE transport, and its schema is validated at mount.
	dshMCPStdio = "stdio"
	dshMCPHTTP  = "streamable-http"
)

// dshMCPServerName is the plugin's serverName schema: the value becomes part
// of every tool name (`mcp__<serverName>__<tool>`).
var dshMCPServerName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// DSHPatchPath returns the DSH home patch layer path. DSH applies the file
// after every profile's own layer, so one file serves every profile.
func DSHPatchPath(home string) string {
	dir, _ := DSHHome(home)

	return filepath.Join(dir, dshPatchFile)
}

// dshMCPSurface writes canon MCP servers into the DSH home patch layer as
// `@deepseek-ai/dsh-mcp-client` records. Only records whose id starts with
// "beadle:" are beadle's: they are grouped by their config.serverName and
// their config is replaced wholesale, while every other entry and comment
// stays untouched.
type dshMCPSurface struct {
	file string
}

func (s *dshMCPSurface) Kind() kind.ID { return kind.MCP }

func (s *dshMCPSurface) Path() string { return s.file }

func (s *dshMCPSurface) WatchPaths() []string { return []string{s.file} }

func (s *dshMCPSurface) Traits() Traits {
	return Traits{
		DefaultMode: config.ModeSync,
		Creatable:   true,
		ReloadHint:  "DSH reloads the home patch layer on live surfaces; the shipped headless/acp/sdk profiles read it on the next run",
		Note:        "MCP servers for every DSH profile, via the home patch layer; beadle writes only records with id beadle:<server>",
	}
}

// Read reports the servers beadle's records mount. A record that carries a
// beadle id but cannot be used (a different plugin name, a bad config, a
// disabled record) is reported as unreadable, so the engine neither pulls its
// config into the canon nor treats it as a deletion; the record itself stays
// untouched and the doctor explains it.
func (s *dshMCPSurface) Read(context.Context) (Snapshot, error) {
	data, present, err := readFile(s.file)
	if err != nil {
		return Snapshot{}, err
	}

	if !present {
		return Snapshot{Items: kind.Items{}}, nil
	}

	doc, err := loadDSHPatch(s.file, data)
	if err != nil {
		return Snapshot{}, err
	}

	in := inspectDSHPatch(doc)
	items := kind.Items{}
	unreadable := map[string]string{}

	var warnings []string

	for _, group := range in.groups {
		switch {
		case group.blocked != "" && group.server == "":
			warnings = append(warnings, group.blocked)
		case group.blocked != "":
			unreadable[group.server] = group.blocked
		default:
			items[group.server] = mcp.Encode(group.last().decoded)
		}
	}

	return Snapshot{Items: items, Present: true, Unreadable: unreadable, Warnings: warnings}, nil
}

// Write applies the desired canon servers to the patch document: records are
// updated in place, missing ones appended as one insert entry, and the records
// of servers the canon dropped removed. A file that needs no change is left
// byte-for-byte as it is.
func (s *dshMCPSurface) Write(_ context.Context, desired kind.Items) error {
	return updateFile(s.file, 0o600, func(data []byte, present bool) ([]byte, bool, error) {
		return s.merge(data, present, desired)
	})
}

func (s *dshMCPSurface) merge(data []byte, present bool, desired kind.Items) ([]byte, bool, error) {
	doc := newDSHPatch()
	indent := 2

	if present {
		loaded, err := loadDSHPatch(s.file, data)
		if err != nil {
			return nil, false, err
		}

		doc = loaded
		indent = dshPatchIndent(doc, data)
	}

	plan := planDSHPatch(doc, desired)
	if !plan.changed() {
		return nil, false, nil
	}

	plan.apply(doc)

	out, err := encodeDSHPatch(doc.doc, indent)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", s.file, err)
	}

	if err := verifyDSHPatch(s.file, out, plan); err != nil {
		return nil, false, err
	}

	return out, true, nil
}

// DSHMCPServerReason reports why a canon MCP server cannot be written to DSH,
// or "" when it can. name is the canon key (the record's serverName); data is
// the canonical server JSON. It is the single source of truth for both the
// writer and the doctor.
func DSHMCPServerReason(name string, data []byte) string {
	if !dshMCPServerName.MatchString(name) {
		return fmt.Sprintf("serverName %q must match %s", name, dshMCPServerName)
	}

	server, err := mcp.Decode(data)
	if err != nil {
		return err.Error()
	}

	server = inferTransport(server)

	if len(server.Command) > 0 && server.URL != "" {
		return "sets both a command and a url; DSH reads one transport per record"
	}

	switch server.Transport {
	case mcp.TransportStdio:
		if len(server.Command) == 0 {
			return "transport stdio needs a command"
		}
	case mcp.TransportHTTP:
		if server.URL == "" {
			return "transport http needs a url"
		}
	default:
		return fmt.Sprintf("transport %q is not supported; DSH reads %s and %s", server.Transport, dshMCPStdio, dshMCPHTTP)
	}

	return ""
}

// dshRecord is one patch record carrying a beadle id.
type dshRecord struct {
	node     *yaml.Node
	id       string
	server   string // config.serverName; "" when it is unreadable
	inInsert bool
	decoded  mcp.Server // valid when reason is ""
	reason   string     // why the record cannot be used; "" when usable
}

// dshServerGroup collects the records of one server name in file order. A
// group with a blocker is reported, never written: the record that carries the
// problem is the user's to fix.
type dshServerGroup struct {
	server  string
	records []*dshRecord
	blocked string
}

func (g *dshServerGroup) last() *dshRecord { return g.records[len(g.records)-1] }

// dshPatchInspection is what one pass over the patch document found: beadle's
// records grouped by server name, the beadle ids in use and the server names
// other records mount.
type dshPatchInspection struct {
	records []*dshRecord
	groups  []*dshServerGroup
	byID    map[string]int    // beadle id -> record count
	foreign map[string]string // serverName -> the record of another owner
}

// inspectDSHPatch walks the patch entries and groups the records that carry a
// beadle id. Only records under a root entry's `insert` list are mounted by
// DSH; a beadle-id record anywhere else is inert and flagged.
func inspectDSHPatch(doc *dshPatchDocument) *dshPatchInspection {
	in := &dshPatchInspection{byID: map[string]int{}, foreign: map[string]string{}}
	index := map[string]*dshServerGroup{}

	for _, entry := range doc.root.Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}

		// The patch entry itself can be a record (an id-targeted override);
		// only the values of its `insert` key hold mounted records.
		in.add(index, entry, false)

		insert := dshMapValue(entry, "insert")

		for i := 0; i+1 < len(entry.Content); i += 2 {
			value := entry.Content[i+1]

			if insert != nil && value == insert && insert.Kind == yaml.SequenceNode {
				in.scanSequence(index, value, true)

				continue
			}

			in.scanValue(index, value, false)
		}
	}

	return in
}

// scanSequence reads a sequence: every mapping item is a row — an inserted
// record or a patch entry — and its values may hold nested rows (a group's
// config list).
func (in *dshPatchInspection) scanSequence(index map[string]*dshServerGroup, seq *yaml.Node, inInsert bool) {
	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			in.scanValue(index, item, inInsert)

			continue
		}

		in.add(index, item, inInsert)

		for i := 1; i < len(item.Content); i += 2 {
			in.scanValue(index, item.Content[i], inInsert)
		}
	}
}

// scanValue walks a mapping value: nested sequences still hold rows (a group's
// config list), nested mappings are only descended into. DSH mounts sequence
// rows only, so a mapping that is a value is never a record — and
// dshDropRecords could not remove one either.
func (in *dshPatchInspection) scanValue(index map[string]*dshServerGroup, node *yaml.Node, inInsert bool) {
	switch node.Kind {
	case yaml.SequenceNode:
		in.scanSequence(index, node, inInsert)
	case yaml.MappingNode:
		for i := 1; i < len(node.Content); i += 2 {
			in.scanValue(index, node.Content[i], inInsert)
		}
	default:
		// Scalars, aliases and documents carry no records of their own.
	}
}

// add records one node: a beadle-id mapping becomes a record, and any other
// record naming the MCP plugin is remembered as a foreign claim on its
// serverName, so beadle never inserts a second server with that name.
func (in *dshPatchInspection) add(index map[string]*dshServerGroup, node *yaml.Node, inInsert bool) {
	id, idOK := dshScalar(dshMapValue(node, "id"))

	if idOK && strings.HasPrefix(id, dshMCPIDPrefix) {
		rec := dshRecordOf(node, id, inInsert)
		in.byID[id]++
		in.records = append(in.records, rec)
		in.group(index, rec)

		return
	}

	name, _ := dshScalar(dshMapValue(node, "name"))
	if name != dshMCPPlugin {
		return
	}

	if disabled, ok := dshBool(dshMapValue(node, "disabled")); ok && disabled {
		return
	}

	server, ok := dshScalar(dshMapValue(dshMapValue(node, "config"), "serverName"))
	if !ok || server == "" {
		return
	}

	in.foreign[server] = cmp.Or(id, "a record without an id")
}

func (in *dshPatchInspection) group(index map[string]*dshServerGroup, rec *dshRecord) {
	grp, ok := index[rec.server]

	if !ok {
		grp = &dshServerGroup{server: rec.server}
		index[rec.server] = grp
		in.groups = append(in.groups, grp)
	}

	grp.records = append(grp.records, rec)

	if grp.blocked == "" && rec.reason != "" {
		grp.blocked = rec.reason
	}
}

// dshRecordOf reads one beadle-id record. The canon identity is the config's
// serverName, since ids are patch row keys; a record without one still reports
// through its id.
func dshRecordOf(node *yaml.Node, id string, inInsert bool) *dshRecord {
	rec := &dshRecord{node: node, id: id, inInsert: inInsert}

	if config := dshMapValue(node, "config"); config != nil && config.Kind == yaml.MappingNode {
		if server, ok := dshScalar(dshMapValue(config, "serverName")); ok {
			rec.server = server
		}
	}

	rec.reason = rec.check()

	return rec
}

// check reports why the record cannot be used, and decodes it when it can. The
// checks mirror the DSH record semantics: the name is verified like DSH does
// before applying an id-targeted patch, an insert list is what makes a record
// mount, disabled is the loader's off switch, and the config must satisfy the
// plugin schema.
func (r *dshRecord) check() string {
	name, _ := dshScalar(dshMapValue(r.node, "name"))
	if name != dshMCPPlugin {
		return fmt.Sprintf("record id %q carries name %q, not %q", r.id, name, dshMCPPlugin)
	}

	if !r.inInsert {
		return fmt.Sprintf("record id %q sits outside an insert list; DSH mounts only inserted records", r.id)
	}

	if value := dshMapValue(r.node, "disabled"); value != nil {
		disabled, ok := dshBool(value)
		switch {
		case !ok:
			return fmt.Sprintf("record id %q has a disabled value DSH rejects (expected a boolean)", r.id)
		case disabled:
			return fmt.Sprintf("record id %q is disabled; DSH does not mount it", r.id)
		}
	}

	config := dshMapValue(r.node, "config")
	if config == nil || config.Kind != yaml.MappingNode {
		return fmt.Sprintf("record id %q has no config mapping", r.id)
	}

	if r.server == "" {
		return fmt.Sprintf("record id %q has no config.serverName string", r.id)
	}

	decoded, err := dshServerFromConfig(config)
	if err != nil {
		return fmt.Sprintf("record id %q: %v", r.id, err)
	}

	r.decoded = decoded

	return ""
}

// dshServerFromConfig decodes a record config into the canonical server model.
// Only the fields beadle manages are read; the plugin's optional tuning keys
// (toolCallTimeoutMs, failOnStartupError, reconnect, cwd) are not part of the
// canon and are ignored here — they stay in the file until a config rewrite.
func dshServerFromConfig(config *yaml.Node) (mcp.Server, error) {
	transport, ok := dshScalar(dshMapValue(config, "transport"))
	if !ok {
		return mcp.Server{}, errors.New("config.transport must be a string")
	}

	server := mcp.Server{}

	switch transport {
	case dshMCPStdio:
		command, ok := dshScalar(dshMapValue(config, "command"))
		if !ok || command == "" {
			return mcp.Server{}, errors.New("config.command must be a non-empty string")
		}

		args, err := dshStringList(dshMapValue(config, "args"))
		if err != nil {
			return mcp.Server{}, fmt.Errorf("config.args %w", err)
		}

		env, err := dshStringMap(dshMapValue(config, "env"))
		if err != nil {
			return mcp.Server{}, fmt.Errorf("config.env %w", err)
		}

		server.Command = append([]string{command}, args...)
		server.Transport = mcp.TransportStdio
		server.Env = env
	case dshMCPHTTP:
		url, ok := dshScalar(dshMapValue(config, "url"))
		if !ok || url == "" {
			return mcp.Server{}, errors.New("config.url must be a non-empty string")
		}

		headers, err := dshStringMap(dshMapValue(config, "headers"))
		if err != nil {
			return mcp.Server{}, fmt.Errorf("config.headers %w", err)
		}

		server.Transport = mcp.TransportHTTP
		server.URL = url
		server.Headers = headers
	default:
		return mcp.Server{}, fmt.Errorf("config.transport %q is not %s or %s", transport, dshMCPStdio, dshMCPHTTP)
	}

	return server, nil
}

// dshStringList reads a sequence of strings; nil means an absent key.
func dshStringList(node *yaml.Node) ([]string, error) {
	if node == nil {
		return nil, nil
	}

	if node.Kind != yaml.SequenceNode {
		return nil, errors.New("must be a list of strings")
	}

	out := make([]string, 0, len(node.Content))

	for _, item := range node.Content {
		text, ok := dshScalar(item)
		if !ok {
			return nil, errors.New("must be a list of strings")
		}

		out = append(out, text)
	}

	return out, nil
}

// dshStringMap reads a mapping of strings; nil means an absent key.
func dshStringMap(node *yaml.Node) (map[string]string, error) {
	if node == nil {
		return nil, nil
	}

	if node.Kind != yaml.MappingNode {
		return nil, errors.New("must be a mapping of strings")
	}

	out := make(map[string]string, len(node.Content)/2)

	for i := 0; i+1 < len(node.Content); i += 2 {
		key, ok := dshScalar(node.Content[i])
		if !ok {
			return nil, errors.New("must be a mapping of strings")
		}

		value, ok := dshScalar(node.Content[i+1])
		if !ok {
			return nil, errors.New("must be a mapping of strings")
		}

		out[key] = value
	}

	return out, nil
}

// dshConfigNode renders a canon server as the plugin config mapping.
func dshConfigNode(name string, server mcp.Server) *yaml.Node {
	pairs := []*yaml.Node{
		dshString("transport"), dshString(dshMCPTransportName(server)),
		dshString("serverName"), dshString(name),
	}

	if server.Remote() {
		pairs = append(pairs, dshString("url"), dshString(server.URL))

		if len(server.Headers) > 0 {
			pairs = append(pairs, dshString("headers"), dshStringMapNode(server.Headers))
		}

		return dshMapping(pairs...)
	}

	pairs = append(pairs, dshString("command"), dshString(server.Command[0]))

	if len(server.Command) > 1 {
		args := make([]*yaml.Node, 0, len(server.Command)-1)
		for _, arg := range server.Command[1:] {
			args = append(args, dshString(arg))
		}

		pairs = append(pairs, dshString("args"), dshSequence(args...))
	}

	if len(server.Env) > 0 {
		pairs = append(pairs, dshString("env"), dshStringMapNode(server.Env))
	}

	return dshMapping(pairs...)
}

// dshMCPTransportName names the plugin transport of a valid canon server.
func dshMCPTransportName(server mcp.Server) string {
	if server.Remote() {
		return dshMCPHTTP
	}

	return dshMCPStdio
}

// dshStringMapNode builds a block mapping node from sorted string pairs.
func dshStringMapNode(values map[string]string) *yaml.Node {
	pairs := make([]*yaml.Node, 0, 2*len(values))

	for _, key := range slices.Sorted(maps.Keys(values)) {
		pairs = append(pairs, dshString(key), dshString(values[key]))
	}

	return dshMapping(pairs...)
}

// dshRecordNode builds the patch record beadle owns for a server.
func dshRecordNode(name string, server mcp.Server) *yaml.Node {
	return dshMapping(
		dshString("id"), dshString(dshMCPIDPrefix+name),
		dshString("name"), dshString(dshMCPPlugin),
		dshString("config"), dshConfigNode(name, server),
	)
}

// dshInsertEntry wraps records into one root-level insert patch entry.
func dshInsertEntry(records []*yaml.Node) *yaml.Node {
	return dshMapping(dshString("insert"), dshSequence(records...))
}

// DSHMCPBlockers reports, per server name, why beadle cannot deliver that
// server to the DSH home patch layer right now: a beadle record that cannot be
// used, a foreign record using the same serverName, or a beadle id already
// held by a record mounting another name (an id can only be inserted once).
// Keys are canon server names, with the beadle id suffix as the fallback for a
// record whose config names no server. A missing file has no blockers; a
// damaged file returns a parse error (the dry-run sync reports those). Only
// this file is inspected: a serverName mounted by a bundle or profile layer
// outside it is invisible here.
func DSHMCPBlockers(home string) (map[string]string, error) {
	path := DSHPatchPath(home)

	data, present, err := readFile(path)
	if err != nil || !present {
		return nil, err
	}

	doc, err := loadDSHPatch(path, data)
	if err != nil {
		return nil, err
	}

	in := inspectDSHPatch(doc)
	blockers := map[string]string{}

	delivered := map[string]bool{}

	for _, group := range in.groups {
		if group.blocked == "" && group.server != "" {
			delivered[group.server] = true
		}
	}

	for _, rec := range in.records {
		suffix := dshIDServer(rec.id)

		if rec.reason != "" {
			setDSHBlocker(blockers, cmp.Or(rec.server, suffix), rec.reason)

			// A blocked record also holds its beadle id until the user fixes
			// or removes it.
			if suffix != rec.server {
				setDSHBlocker(blockers, suffix, rec.reason)
			}

			continue
		}

		if suffix != rec.server && !delivered[suffix] {
			setDSHBlocker(blockers, suffix, fmt.Sprintf("id %q is held by the record mounting %q", rec.id, rec.server))
		}
	}

	for _, server := range slices.Sorted(maps.Keys(in.foreign)) {
		setDSHBlocker(blockers, server, fmt.Sprintf("serverName %q is already used by %s, which beadle does not own", server, in.foreign[server]))
	}

	return blockers, nil
}

// setDSHBlocker keeps the first reason per server name: records are visited in
// file order, and the first record that blocks a name explains it.
func setDSHBlocker(blockers map[string]string, key, reason string) {
	if key == "" {
		return
	}

	if _, taken := blockers[key]; !taken {
		blockers[key] = reason
	}
}

func dshIDServer(id string) string {
	return strings.TrimPrefix(id, dshMCPIDPrefix)
}

// dshConfigUpdate replaces the config of one existing record.
type dshConfigUpdate struct {
	record *dshRecord
	server mcp.Server
}

// dshInsert adds one new record under a new insert entry.
type dshInsert struct {
	name   string
	server mcp.Server
}

// dshPatchPlan is the edit set for one patch document. expected holds every
// server this write delivers, in the canonical form the surface reads back;
// beforeIDs and removedIDs let the verification prove the record set changed
// exactly as planned.
type dshPatchPlan struct {
	updates    []dshConfigUpdate
	removals   map[*yaml.Node]bool
	inserts    []dshInsert
	expected   kind.Items
	beforeIDs  map[string]int
	removedIDs map[string]int
}

func (p *dshPatchPlan) changed() bool {
	return len(p.updates) > 0 || len(p.removals) > 0 || len(p.inserts) > 0
}

// planDSHPatch maps the desired canon items onto the inspected document.
// Records of servers no longer in the canon are removed; unusable records and
// server names another record already mounts are left alone, and a server
// whose beadle id is taken is never inserted twice.
func planDSHPatch(doc *dshPatchDocument, desired kind.Items) *dshPatchPlan {
	in := inspectDSHPatch(doc)
	plan := &dshPatchPlan{
		removals:   map[*yaml.Node]bool{},
		expected:   kind.Items{},
		beforeIDs:  maps.Clone(in.byID),
		removedIDs: map[string]int{},
	}

	grouped := map[string]bool{}

	for _, group := range in.groups {
		grouped[group.server] = true
		planDSHGroup(plan, group, desired)
	}

	planDSHInserts(plan, in, desired, grouped)

	return plan
}

// planDSHGroup maps one group of existing records onto the canon: a server the
// canon dropped, or one whose current shape DSH cannot express, loses its
// records; a changed server gets its config rewritten record by record; an
// unchanged one is only marked expected.
func planDSHGroup(plan *dshPatchPlan, group *dshServerGroup, desired kind.Items) {
	if group.blocked != "" || group.server == "" {
		return
	}

	data, wanted := desired[group.server]
	if !wanted || DSHMCPServerReason(group.server, data) != "" {
		// The canon dropped the server, or DSH cannot express its current
		// shape: the record goes. A record that cannot be used is left alone.
		for _, rec := range group.records {
			plan.removals[rec.node] = true
			plan.removedIDs[rec.id]++
		}

		return
	}

	server, err := mcp.Decode(data)
	if err != nil {
		return
	}

	server = inferTransport(server)
	want := mcp.Encode(server)

	for _, rec := range group.records {
		if !bytes.Equal(mcp.Encode(rec.decoded), want) {
			plan.updates = append(plan.updates, dshConfigUpdate{record: rec, server: server})
		}
	}

	plan.expected[group.server] = want
}

// planDSHInserts adds the canon servers the file does not carry yet. A server
// whose beadle id a surviving record keeps, or whose serverName another record
// already uses, is left out; the doctor reports those.
func planDSHInserts(plan *dshPatchPlan, in *dshPatchInspection, desired kind.Items, grouped map[string]bool) {
	for _, name := range desired.Keys() {
		// A beadle id that only records this same write removes becomes free
		// again; an id a blocked or surviving record keeps is never reused.
		id := dshMCPIDPrefix + name

		if grouped[name] || in.byID[id] > plan.removedIDs[id] || in.foreign[name] != "" {
			continue
		}

		if DSHMCPServerReason(name, desired[name]) != "" {
			continue
		}

		server, err := mcp.Decode(desired[name])
		if err != nil {
			continue
		}

		server = inferTransport(server)
		plan.inserts = append(plan.inserts, dshInsert{name: name, server: server})
		plan.expected[name] = mcp.Encode(server)
	}
}

// apply mutates the document: configs are replaced in place, records are
// dropped with the entries they emptied, and new records land in one appended
// insert entry.
func (p *dshPatchPlan) apply(doc *dshPatchDocument) {
	for _, update := range p.updates {
		dshSetConfig(update.record.node, dshConfigNode(update.record.server, update.server))
	}

	if len(p.removals) > 0 {
		dshDropRecords(doc.root, p.removals)
	}

	if len(p.inserts) > 0 {
		if len(doc.root.Content) == 0 {
			// The shipped template's empty flow `[]` reads better as a block
			// list once it carries records; a populated flow file keeps its
			// own style.
			doc.root.Style = 0
		}

		records := make([]*yaml.Node, 0, len(p.inserts))

		for _, insert := range p.inserts {
			records = append(records, dshRecordNode(insert.name, insert.server))
		}

		doc.root.Content = append(doc.root.Content, dshInsertEntry(records))
	}
}

// verifyDSHPatch re-parses the encoded bytes and proves the write did what the
// plan said: every expected server decodes back to the desired bytes, no
// usable record appeared that the plan did not deliver, and the set of beadle
// ids changed exactly by the planned removals and inserts. A failed
// verification refuses the write instead of leaving a file the writer cannot
// account for.
func verifyDSHPatch(path string, out []byte, plan *dshPatchPlan) error {
	doc, err := loadDSHPatch(path, out)
	if err != nil {
		return fmt.Errorf("verify %s: %w", path, err)
	}

	in := inspectDSHPatch(doc)

	for _, group := range in.groups {
		if group.blocked != "" {
			continue
		}

		want, ok := plan.expected[group.server]
		if !ok || !bytes.Equal(mcp.Encode(group.last().decoded), want) {
			return fmt.Errorf("verify %s: server %q did not land as desired", path, group.server)
		}
	}

	for name := range plan.expected {
		group := in.groupOf(name)
		if group == nil || group.blocked != "" {
			return fmt.Errorf("verify %s: server %q did not land as desired", path, name)
		}
	}

	wantIDs := maps.Clone(plan.beforeIDs)

	for id, count := range plan.removedIDs {
		wantIDs[id] -= count

		if wantIDs[id] <= 0 {
			delete(wantIDs, id)
		}
	}

	for _, insert := range plan.inserts {
		wantIDs[dshMCPIDPrefix+insert.name]++
	}

	if !maps.Equal(wantIDs, in.byID) {
		return fmt.Errorf("verify %s: the beadle records changed beyond the plan", path)
	}

	return nil
}

func (in *dshPatchInspection) groupOf(server string) *dshServerGroup {
	for _, group := range in.groups {
		if group.server == server {
			return group
		}
	}

	return nil
}
