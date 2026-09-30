// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package store

import (
	"context"
	"fmt"
)

// ReanalyzeResult counts what a re-analysis changed.
type ReanalyzeResult struct {
	Answers             int
	MentionsBefore      int
	MentionsAfter       int
	PromptsReclassified int
}

// Reanalyze rebuilds the rows derived from every stored answer: its mentions
// and its classified citations, from the answer text and the citations the
// engine gave, and every prompt's branded flag. It is one transaction, so
// the dashboard shows the old reading or the new one, never a mix.
//
// The derivation is passed in because it belongs to the runner: analyze is
// exactly what a new pass stores for an answer, so a re-read answer and a
// fresh one cannot disagree. The raw answer and its fan-out are not touched.
func (db *DB) Reanalyze(ctx context.Context, analyze func(text string, cited []Citation) ([]Mention, []Citation), branded func(text string) bool) (ReanalyzeResult, error) {
	var res ReanalyzeResult
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	defer tx.Rollback()

	type answer struct {
		id   int64
		text string
	}
	var answers []answer
	rows, err := tx.QueryContext(ctx, `SELECT id, text FROM chat WHERE status = 'ok' ORDER BY id`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var a answer
		if err := rows.Scan(&a.id, &a.text); err != nil {
			rows.Close()
			return res, err
		}
		answers = append(answers, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	cited := map[int64][]Citation{}
	rows, err = tx.QueryContext(ctx, `SELECT chat_id, url, host, site, title, position, source_type FROM citation ORDER BY chat_id, position, id`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var chatID int64
		var c Citation
		if err := rows.Scan(&chatID, &c.URL, &c.Host, &c.Site, &c.Title, &c.Position, &c.SourceType); err != nil {
			rows.Close()
			return res, err
		}
		cited[chatID] = append(cited[chatID], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mention`).Scan(&res.MentionsBefore); err != nil {
		return res, err
	}
	for _, a := range answers {
		mentions, citations := analyze(a.text, cited[a.id])
		if _, err := tx.ExecContext(ctx, `DELETE FROM mention WHERE chat_id = ?`, a.id); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM citation WHERE chat_id = ?`, a.id); err != nil {
			return res, err
		}
		for _, m := range mentions {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO mention (chat_id, competitor_id, brand_key, brand_name, offset_start, offset_end, list_rank)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				a.id, m.CompetitorID, m.BrandKey, m.BrandName, m.OffsetStart, m.OffsetEnd, m.ListRank); err != nil {
				return res, fmt.Errorf("insert mention: %w", err)
			}
		}
		for _, c := range citations {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO citation (chat_id, url, host, site, title, position, source_type)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				a.id, c.URL, c.Host, c.Site, c.Title, c.Position, c.SourceType); err != nil {
				return res, fmt.Errorf("insert citation: %w", err)
			}
		}
		res.Answers++
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM mention`).Scan(&res.MentionsAfter); err != nil {
		return res, err
	}

	type prompt struct {
		id      int64
		text    string
		branded int
	}
	var prompts []prompt
	rows, err = tx.QueryContext(ctx, `SELECT id, text, branded FROM prompt`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var p prompt
		if err := rows.Scan(&p.id, &p.text, &p.branded); err != nil {
			rows.Close()
			return res, err
		}
		prompts = append(prompts, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, p := range prompts {
		b := 0
		if branded(p.text) {
			b = 1
		}
		if b != p.branded {
			if _, err := tx.ExecContext(ctx, `UPDATE prompt SET branded = ? WHERE id = ?`, b, p.id); err != nil {
				return res, err
			}
			res.PromptsReclassified++
		}
	}
	return res, tx.Commit()
}
