// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

// upgrade_to_cloud. The one tool whose job is to make this server stop being
// the right one to talk to.

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/upgrade"
)

type upgradeArgs struct {
	Key   string `json:"key,omitempty" jsonschema:"a Limelit Cloud API key from limelit.co/settings; omit to see what Cloud adds without moving anything"`
	Since string `json:"since,omitempty" jsonschema:"only send answers created on or after this date, YYYY-MM-DD"`
}

type upgradeOut struct {
	Moved bool `json:"moved"`
	// Adds is what Cloud has that this instance does not, so an agent asked
	// for a Cloud-only feature can answer without guessing.
	Adds []string `json:"cloud_adds,omitempty"`
	// HowTo is present when nothing moved, so the next step is on screen.
	HowTo string `json:"how_to,omitempty"`

	Prompts      int    `json:"prompts_imported,omitempty"`
	Competitors  int    `json:"competitors_imported,omitempty"`
	Chats        int    `json:"chats_imported,omitempty"`
	Mentions     int    `json:"mentions_imported,omitempty"`
	Citations    int    `json:"citations_imported,omitempty"`
	ChatsSkipped int    `json:"chats_skipped,omitempty"`
	WorkspaceURL string `json:"workspace_url,omitempty"`
	MCPURL       string `json:"mcp_url,omitempty"`
	// SwitchTo tells the client to point at Cloud from now on.
	SwitchTo string `json:"switch_to,omitempty"`
}

// cloudAdds is the boundary, stated once. It is the answer when a user asks
// this server for something only Cloud measures.
var cloudAdds = []string{
	"Sentiment and framing, hallucination guard, correction drafts",
	"Query fan-out rewrite analysis, on top of the capture this instance already does",
	"Prompt generation from your site, personas, competitor discovery",
	"Perception audits",
	"Google Search Console and GA4",
	"Segments, portfolios, multi-brand and white-label",
	"Agents, sheets and blocks",
	"Teams: roles, invites, SSO, one bill",
	"Scheduled runs someone else operates, with the scrapers kept working",
}

func registerUpgrade(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "upgrade_to_cloud",
		Description: "Move this self-hosted instance to Limelit Cloud: uploads every prompt, competitor, " +
			"answer, mention and citation to a Cloud workspace and returns its MCP endpoint. Nothing is " +
			"deleted here and provider keys are never sent. Called without a key it explains what Cloud " +
			"adds and how to get one, moving nothing. This is the right tool when someone asks for " +
			"sentiment, segments, portfolios, Search Console or anything else this server has no tool for.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in upgradeArgs) (*mcp.CallToolResult, upgradeOut, error) {
		key := strings.TrimSpace(in.Key)
		if key == "" {
			return nil, upgradeOut{
				Moved: false,
				Adds:  cloudAdds,
				HowTo: "Create an API key at https://limelit.co/settings, then call this tool again with it as `key`. " +
					"Everything here is uploaded and nothing is deleted, so the move is reversible and re-running is safe.",
			}, nil
		}

		result, err := upgrade.Run(ctx, d.DB, upgrade.Options{Key: key, Since: in.Since})
		if err != nil {
			return nil, upgradeOut{}, err
		}

		return nil, upgradeOut{
			Moved:        true,
			Prompts:      result.Import.Prompts,
			Competitors:  result.Import.Competitors,
			Chats:        result.Import.Chats,
			Mentions:     result.Import.Mentions,
			Citations:    result.Import.Citations,
			ChatsSkipped: result.Import.ChatsSkipped,
			WorkspaceURL: result.WorkspaceURL,
			MCPURL:       result.MCPURL,
			SwitchTo: fmt.Sprintf(
				"Point this client at %s with the same API key from now on. This instance still holds everything and can be run again.",
				result.MCPURL),
		}, nil
	})
}
