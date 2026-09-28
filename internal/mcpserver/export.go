// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// export_data. Open-core only: Limelit Cloud has its own export, and this one
// exists so leaving is a command rather than a support ticket.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/export"
)

// exportInlineCap is the most an export may carry back through a tool call.
//
// An instance running daily across seven engines outgrows any inline limit,
// and a tool that silently truncates would hand an agent a partial export it
// believed was whole. Past the cap the tool returns the counts and the exact
// command to run instead, which is the honest failure.
const exportInlineCap = 512 << 10

type exportArgs struct {
	Format string `json:"format,omitempty" jsonschema:"json only through this tool; csv is written by the CLI"`
	Since  string `json:"since,omitempty" jsonschema:"only answers created on or after this date, YYYY-MM-DD"`
}

type exportOut struct {
	SchemaVersion int    `json:"schema_version"`
	ExportedAt    string `json:"exported_at"`
	Since         string `json:"since,omitempty"`
	Bytes         int    `json:"bytes"`
	// Truncated is never true. Either the whole payload is here or it is
	// absent with a reason, because a partial export that looks whole is the
	// one outcome worth preventing.
	Complete bool           `json:"complete"`
	Counts   map[string]int `json:"counts"`
	// Payload is the export document, present only when it fits.
	//
	// A decoded object rather than raw bytes, because the SDK infers a JSON
	// schema from this type and json.RawMessage infers as an array, which
	// then fails validation against the object an export actually is.
	Payload map[string]any `json:"payload,omitempty"`
	// Note explains what to run when the payload did not fit.
	Note string `json:"note,omitempty"`
}

func registerExport(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "export_data",
		Description: "Export everything this instance holds: the property, competitors, prompts, targets, " +
			"evaluations, answers, mentions, citations, query fan-out and usage. This is the same payload " +
			"the Cloud upgrade sends, so what survives an upgrade is exactly what you can read here. Large " +
			"exports return their row counts and the command to run rather than a truncated payload.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in exportArgs) (*mcp.CallToolResult, exportOut, error) {
		switch in.Format {
		case "", "json":
		case "csv":
			return nil, exportOut{}, fmt.Errorf(
				"csv is written to a directory of files, which a tool call cannot return. Run: limelit export --format csv --out ./limelit-export")
		default:
			return nil, exportOut{}, fmt.Errorf("format must be json")
		}

		now := time.Now().UTC().Format(time.RFC3339)
		var buf bytes.Buffer
		if err := export.WriteJSON(ctx, d.DB, &buf, export.Options{Since: in.Since, Now: now}); err != nil {
			return nil, exportOut{}, err
		}

		out := exportOut{
			SchemaVersion: export.SchemaVersion,
			ExportedAt:    now,
			Since:         in.Since,
			Bytes:         buf.Len(),
			Counts:        map[string]int{},
		}

		// Counts come from the document just written rather than from a
		// second set of queries, so they describe this payload and not the
		// database a moment later.
		var doc map[string]any
		if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
			return nil, exportOut{}, fmt.Errorf("export produced invalid JSON: %w", err)
		}
		for name, value := range doc {
			if rows, ok := value.([]any); ok {
				out.Counts[name] = len(rows)
			}
		}

		if buf.Len() > exportInlineCap {
			out.Complete = false
			out.Note = fmt.Sprintf(
				"The export is %d KB, over the %d KB a tool call carries. The counts above are the whole export. "+
					"Run: limelit export --format json --out limelit-export.json",
				buf.Len()>>10, exportInlineCap>>10)
			return nil, out, nil
		}

		out.Complete = true
		out.Payload = doc
		return nil, out, nil
	})
}
