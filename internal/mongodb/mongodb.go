// Package mongodb provides database/collection browsing, document querying, and
// a guarded command console over a MongoDB client. It is the document-store
// analog of internal/redisdb: a non-SQL engine that lives outside the
// dbsql.Engine interface and is reached through its own registry getter and its
// own /api/c/{id}/mongo/* endpoints.
package mongodb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	opTimeout = 30 * time.Second
	maxDocs   = 1000
	pageDocs  = 50
)

// systemDBs are MongoDB's internal databases, hidden from the explorer the way
// the SQL engines hide system schemas.
var systemDBs = map[string]bool{"admin": true, "local": true, "config": true}

// DBInfo is one database with the names of its collections.
type DBInfo struct {
	Name        string   `json:"Name"`
	Collections []string `json:"Collections"`
}

// IndexInfo describes one index on a collection.
type IndexInfo struct {
	Name   string `json:"Name"`
	Keys   string `json:"Keys"`
	Unique bool   `json:"Unique"`
}

// DocPage is a page of documents (each pretty-printed relaxed extended JSON)
// plus whether more documents exist past this page.
type DocPage struct {
	Docs    []string `json:"docs"`
	HasMore bool     `json:"hasMore"`
}

// Databases lists non-system databases and their collection names.
func Databases(ctx context.Context, client *mongo.Client) ([]DBInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	names, err := client.ListDatabaseNames(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var out []DBInfo
	for _, name := range names {
		if systemDBs[name] {
			continue
		}
		colls, err := client.Database(name).ListCollectionNames(ctx, bson.D{})
		if err != nil {
			return nil, err
		}
		sort.Strings(colls)
		out = append(out, DBInfo{Name: name, Collections: colls})
	}
	return out, nil
}

// Indexes lists a collection's indexes, preserving compound-key order.
func Indexes(ctx context.Context, client *mongo.Client, db, coll string) ([]IndexInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cur, err := client.Database(db).Collection(coll).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []IndexInfo
	for cur.Next(ctx) {
		ix := IndexInfo{}
		if name, ok := cur.Current.Lookup("name").StringValueOK(); ok {
			ix.Name = name
		}
		if u, ok := cur.Current.Lookup("unique").BooleanOK(); ok {
			ix.Unique = u
		}
		if keyDoc, ok := cur.Current.Lookup("key").DocumentOK(); ok {
			if elems, err := keyDoc.Elements(); err == nil {
				parts := make([]string, 0, len(elems))
				for _, e := range elems {
					parts = append(parts, e.Key()+": "+rawValStr(e.Value()))
				}
				ix.Keys = strings.Join(parts, ", ")
			}
		}
		out = append(out, ix)
	}
	return out, cur.Err()
}

// Find returns a page of documents matching the (relaxed extended JSON) filter,
// with optional sort and projection. limit/skip drive pagination; results are
// capped at maxDocs. One extra document is fetched to report HasMore without a
// separate count.
func Find(ctx context.Context, client *mongo.Client, db, coll, filterJSON, sortJSON, projJSON string, limit, skip int64) (*DocPage, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	if limit <= 0 || limit > maxDocs {
		limit = pageDocs
	}
	if skip < 0 {
		skip = 0
	}
	filter, err := parseDoc(filterJSON)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}

	opts := options.Find().SetLimit(limit + 1).SetSkip(skip)
	if s := strings.TrimSpace(sortJSON); s != "" {
		var sortDoc bson.D
		if err := bson.UnmarshalExtJSON([]byte(s), false, &sortDoc); err != nil {
			return nil, fmt.Errorf("sort: %w", err)
		}
		opts.SetSort(sortDoc)
	}
	if p := strings.TrimSpace(projJSON); p != "" {
		proj, err := parseDoc(p)
		if err != nil {
			return nil, fmt.Errorf("projection: %w", err)
		}
		opts.SetProjection(proj)
	}

	cur, err := client.Database(db).Collection(coll).Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	page := &DocPage{}
	for cur.Next(ctx) {
		if int64(len(page.Docs)) >= limit {
			page.HasMore = true // the extra (limit+1) document proves another page exists
			break
		}
		page.Docs = append(page.Docs, prettyJSON(cur.Current))
	}
	if err := cur.Err(); err != nil {
		return page, err
	}
	return page, nil
}

// ── command console screening ──

// readAllow is the set of MongoDB commands permitted on read-only connections
// (or for users without write access). Mirrors redisReadAllow.
var readAllow = map[string]bool{
	"find": true, "aggregate": true, "count": true, "distinct": true,
	"listcollections": true, "listindexes": true, "listdatabases": true,
	"dbstats": true, "collstats": true, "connectionstatus": true, "explain": true,
	"getmore": true, "ping": true, "hello": true, "ismaster": true,
	"buildinfo": true, "serverstatus": true,
}

// dangerous commands can drop data, run server-side JavaScript, or take over the
// server: they are admin-only AND confirm-gated, matching the Redis console's
// NeedsConfirm policy. mapreduce/eval run arbitrary JS, so they belong here too.
var dangerous = map[string]bool{
	"drop": true, "dropdatabase": true, "dropindexes": true, "dropconnections": true,
	"shutdown": true, "fsync": true, "killallsessions": true, "killallsessionsbypattern": true,
	"killop": true, "killcursors": true, "setparameter": true, "setfeaturecompatibilityversion": true,
	"replsetreconfig": true, "replsetstepdown": true, "logrotate": true, "flushrouterconfig": true,
	"mapreduce": true, "eval": true,
}

// serverJSKeys are query operators that execute server-side JavaScript. They run
// arbitrary JS and force full-collection scans, so they are a DoS and a way to
// read fields a projection would hide. Non-admins are blocked from using them in
// a find filter (the command console already gates mapreduce/eval via dangerous).
var serverJSKeys = map[string]bool{"$where": true, "$function": true, "$accumulator": true}

// UsesServerJS reports whether any of the supplied relaxed-extended-JSON
// documents (filter/sort/projection) contain a server-side JavaScript operator
// ($where/$function/$accumulator) at any depth. Relaxed extended JSON is still
// valid JSON, so a plain structural walk suffices to spot the operator keys; an
// unparseable document is treated as JS-free so Find can surface the real error.
func UsesServerJS(docs ...string) bool {
	for _, d := range docs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		var v any
		if json.Unmarshal([]byte(d), &v) != nil {
			continue
		}
		if walkServerJS(v) {
			return true
		}
	}
	return false
}

func walkServerJS(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			if serverJSKeys[strings.ToLower(k)] || walkServerJS(sub) {
				return true
			}
		}
	case []any:
		for _, sub := range t {
			if walkServerJS(sub) {
				return true
			}
		}
	}
	return false
}

// CommandName parses a relaxed-extended-JSON command document and returns its
// command (the first field, lowercased), which MongoDB uses to dispatch.
func CommandName(cmdJSON string) (string, error) {
	cmd, err := parseOrdered(cmdJSON)
	if err != nil {
		return "", err
	}
	if len(cmd) == 0 {
		return "", fmt.Errorf("empty command")
	}
	return strings.ToLower(cmd[0].Key), nil
}

// ReadAllowed reports whether a command is a read that read-only users may run.
func ReadAllowed(name string) bool { return readAllow[name] }

// NeedsConfirm reports whether a command is destructive enough to require admin
// plus an explicit confirmation.
func NeedsConfirm(name string) bool { return dangerous[name] }

// RunCommand runs a database command supplied as relaxed extended JSON and
// returns the result as pretty-printed JSON.
func RunCommand(ctx context.Context, client *mongo.Client, db, cmdJSON string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	cmd, err := parseOrdered(cmdJSON)
	if err != nil {
		return "", err
	}
	if len(cmd) == 0 {
		return "", fmt.Errorf("empty command")
	}
	raw, err := client.Database(db).RunCommand(ctx, cmd).Raw()
	if err != nil {
		return "", err
	}
	return prettyJSON(raw), nil
}

// ── helpers ──

// parseDoc parses relaxed extended JSON into an unordered document (filter /
// projection: key order is irrelevant). An empty string is the empty document.
func parseDoc(s string) (bson.M, error) {
	if strings.TrimSpace(s) == "" {
		return bson.M{}, nil
	}
	var m bson.M
	if err := bson.UnmarshalExtJSON([]byte(s), false, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// parseOrdered parses relaxed extended JSON into an ordered document (commands:
// the first field is the command name, so order matters).
func parseOrdered(s string) (bson.D, error) {
	var d bson.D
	if err := bson.UnmarshalExtJSON([]byte(s), false, &d); err != nil {
		return nil, err
	}
	return d, nil
}

// prettyJSON renders a BSON value (bson.Raw) as indented relaxed extended JSON.
// On any marshal error it falls back to the compact form or the Go value.
func prettyJSON(v any) string {
	b, err := bson.MarshalExtJSON(v, false, false)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return string(b)
	}
	return out.String()
}

// rawValStr renders an index key direction/type (1, -1, "text", "2dsphere", …).
func rawValStr(rv bson.RawValue) string {
	if i, ok := rv.Int32OK(); ok {
		return strconv.FormatInt(int64(i), 10)
	}
	if i, ok := rv.Int64OK(); ok {
		return strconv.FormatInt(i, 10)
	}
	if f, ok := rv.DoubleOK(); ok {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	if s, ok := rv.StringValueOK(); ok {
		return s
	}
	return rv.String()
}
