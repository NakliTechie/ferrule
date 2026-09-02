package store

import "strings"

// Entry is one metadata-only ledger row. Prompt and response content are never stored
// here; content logging is a separate, off-by-default, local-only path (§4.5).
type Entry struct {
	ID               int64   `json:"id"`
	TS               int64   `json:"ts"`
	GrantID          string  `json:"grant_id"`
	App              string  `json:"app"`
	SourceID         string  `json:"source_id"`
	Provider         string  `json:"provider"`
	ModelID          string  `json:"model_id"`
	RequestedModel   string  `json:"requested_model"`
	Lane             string  `json:"lane"`
	Egress           string  `json:"egress"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
	LatencyMS        int     `json:"latency_ms"`
	Status           int     `json:"status"`
	Err              string  `json:"err"`
	ReqBytes         int     `json:"req_bytes"`
	RespBytes        int     `json:"resp_bytes"`
}

// Record appends a ledger row and returns its id.
//
// The id is returned rather than looked up afterwards: reading MAX(id) as a separate
// statement is a race, and under concurrent requests it attaches one call's prompt to
// another call's row — the single worst way for a content log to be wrong.
func (d *DB) Record(e Entry) (int64, error) {
	if e.TS == 0 {
		e.TS = now()
	}
	if e.Lane == "" {
		e.Lane = LaneTokens
	}
	if e.Egress == "" {
		e.Egress = EgressLocal
	}
	res, err := d.sql.Exec(`
INSERT INTO ledger (ts,grant_id,app,source_id,provider,model_id,requested_model,lane,egress,
                    prompt_tokens,completion_tokens,cost,latency_ms,status,err,req_bytes,resp_bytes)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		e.TS, e.GrantID, e.App, e.SourceID, e.Provider, e.ModelID, e.RequestedModel, e.Lane, e.Egress,
		e.PromptTokens, e.CompletionTokens, e.Cost, e.LatencyMS, e.Status, e.Err, e.ReqBytes, e.RespBytes)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Entries returns the most recent rows, newest first.
func (d *DB) Entries(limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := d.sql.Query(`
SELECT id,ts,grant_id,app,source_id,provider,model_id,requested_model,lane,egress,
       prompt_tokens,completion_tokens,cost,latency_ms,status,err,req_bytes,resp_bytes
FROM ledger ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TS, &e.GrantID, &e.App, &e.SourceID, &e.Provider, &e.ModelID,
			&e.RequestedModel, &e.Lane, &e.Egress, &e.PromptTokens, &e.CompletionTokens, &e.Cost,
			&e.LatencyMS, &e.Status, &e.Err, &e.ReqBytes, &e.RespBytes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Bucket is one aggregated row of the usage view.
type Bucket struct {
	Key              string  `json:"key"`
	App              string  `json:"app"`
	SourceID         string  `json:"source_id"`
	Provider         string  `json:"provider"`
	ModelID          string  `json:"model_id"`
	Egress           string  `json:"egress"`
	Requests         int     `json:"requests"`
	Errors           int     `json:"errors"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`
	AvgLatencyMS     int     `json:"avg_latency_ms"`
	ReqBytes         int     `json:"req_bytes"`
	RespBytes        int     `json:"resp_bytes"`
}

// Aggregate groups the ledger by any combination of app, model, provider, and egress.
// since is a millisecond timestamp; pass 0 for all time.
func (d *DB) Aggregate(by []string, since int64) ([]Bucket, error) {
	allowed := map[string]string{
		"app":      "app",
		"model":    "model_id",
		"source":   "source_id",
		"provider": "provider",
		"egress":   "egress",
	}
	var cols []string
	for _, b := range by {
		c, ok := allowed[b]
		if !ok {
			continue
		}
		cols = append(cols, c)
	}
	if len(cols) == 0 {
		cols = []string{"app"}
	}
	sel := strings.Join(cols, ",")
	q := `
SELECT ` + sel + `,
       COUNT(*), SUM(CASE WHEN status >= 400 OR err <> '' THEN 1 ELSE 0 END),
       SUM(prompt_tokens), SUM(completion_tokens), SUM(cost),
       SUM(req_bytes), SUM(resp_bytes), CAST(AVG(latency_ms) AS INTEGER)
FROM ledger WHERE ts >= ? GROUP BY ` + sel + ` ORDER BY SUM(cost) DESC, COUNT(*) DESC`
	rows, err := d.sql.Query(q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		vals := make([]any, len(cols)+8)
		strs := make([]string, len(cols))
		for i := range cols {
			vals[i] = &strs[i]
		}
		var b Bucket
		vals[len(cols)+0] = &b.Requests
		vals[len(cols)+1] = &b.Errors
		vals[len(cols)+2] = &b.PromptTokens
		vals[len(cols)+3] = &b.CompletionTokens
		vals[len(cols)+4] = &b.Cost
		vals[len(cols)+5] = &b.ReqBytes
		vals[len(cols)+6] = &b.RespBytes
		vals[len(cols)+7] = &b.AvgLatencyMS
		if err := rows.Scan(vals...); err != nil {
			return nil, err
		}
		for i, c := range cols {
			switch c {
			case "app":
				b.App = strs[i]
			case "model_id":
				b.ModelID = strs[i]
			case "source_id":
				b.SourceID = strs[i]
			case "provider":
				b.Provider = strs[i]
			case "egress":
				b.Egress = strs[i]
			}
		}
		b.Key = strings.Join(strs, " / ")
		out = append(out, b)
	}
	return out, rows.Err()
}

// Content is the optional, off-by-default, local-only record of what a request and its
// response actually said (§4.5). It lives in its own table so the metadata ledger stays
// free of content by construction, and it is never included in a configuration export.
type Content struct {
	LedgerID int64  `json:"ledger_id"`
	TS       int64  `json:"ts"`
	App      string `json:"app"`
	Model    string `json:"model"`
	Request  string `json:"request"`
	Response string `json:"response"`
}

// ContentLoggingOn reports whether the person has turned local content logging on.
func (d *DB) ContentLoggingOn() bool {
	return d.Setting(SetContentLogging, "off") == "on"
}

// RecordContent stores one request/response pair. Callers must check ContentLoggingOn
// first; this method does not second-guess them, but it does refuse to run when the
// setting is off, so a stray call cannot start recording content silently.
func (d *DB) RecordContent(c Content) error {
	if !d.ContentLoggingOn() {
		return nil
	}
	_, err := d.sql.Exec(`
INSERT INTO content_log (ledger_id, ts, app, model, request, response) VALUES (?,?,?,?,?,?)`,
		c.LedgerID, now(), c.App, c.Model, c.Request, c.Response)
	return err
}

// Contents returns the most recent logged pairs, newest first.
func (d *DB) Contents(limit int) ([]Content, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.sql.Query(`
SELECT ledger_id, ts, app, model, request, response
FROM content_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Content
	for rows.Next() {
		var c Content
		if err := rows.Scan(&c.LedgerID, &c.TS, &c.App, &c.Model, &c.Request, &c.Response); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ForgetContent deletes every logged pair. Turning the setting off does not delete what
// was already recorded; this is how a person actually gets rid of it.
func (d *DB) ForgetContent() (int64, error) {
	res, err := d.sql.Exec(`DELETE FROM content_log`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Failures returns the most recent rows that actually failed, newest first.
//
// Asked for directly rather than filtered out of a window of recent entries: a window
// loses a failure as soon as enough successes follow it, and that is precisely when
// someone goes looking for it.
func (d *DB) Failures(limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := d.sql.Query(`
SELECT id,ts,grant_id,app,source_id,provider,model_id,requested_model,lane,egress,
       prompt_tokens,completion_tokens,cost,latency_ms,status,err,req_bytes,resp_bytes
FROM ledger WHERE status >= 400 OR err <> '' ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.TS, &e.GrantID, &e.App, &e.SourceID, &e.Provider, &e.ModelID,
			&e.RequestedModel, &e.Lane, &e.Egress, &e.PromptTokens, &e.CompletionTokens, &e.Cost,
			&e.LatencyMS, &e.Status, &e.Err, &e.ReqBytes, &e.RespBytes); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
