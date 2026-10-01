// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
)

// openOnlyArguments is every argument this server takes on a tool Limelit
// Cloud also has, where Cloud's tool does not declare it. Each one is a
// decision, with its reason, because an agent that moves to Cloud keeps
// sending it:
//
//   - On a Cloud tool that spends or writes, Cloud refuses an argument it
//     does not declare, so the agent gets an error naming it. Still, the
//     right move is to add the argument to Cloud first (as dry_run and target
//     were for the run tools), and this list is where skipping that shows.
//   - On a Cloud read tool, Cloud ignores it, and the answer is unfiltered.
//     The reason has to say why that is acceptable, or the entry is a bug
//     waiting for an upgrade.
//
// A new argument on a shared tool fails TestSharedToolsMatchCloud until it is
// either added to Cloud (then testdata/cloud_tools.json is refreshed) or
// recorded here.
var openOnlyArguments = map[string]string{
	"get_matrix.segment":        "declared only so it can be refused with a pointer to Cloud; never accepted here",
	"get_overview_kpis.segment": "declared only so it can be refused with a pointer to Cloud; never accepted here",
	"list_prompts.segment":      "declared only so it can be refused with a pointer to Cloud; never accepted here",
	"list_top_sources.segment":  "declared only so it can be refused with a pointer to Cloud; never accepted here",

	"get_run_activity.evaluation_id": "read; on Cloud the call reports the org's current run activity instead of one run, which is still a true answer",
	"list_chats.show":                "read; KNOWN GAP: Cloud ignores it, so show=missed lists every answer after an upgrade",
	"list_chats.target":              "read; KNOWN GAP: Cloud ignores it, so the list spans every engine after an upgrade",
	"list_top_sources.days":          "read; KNOWN GAP: Cloud ignores it, so the ranking is all-time after an upgrade",
}

type cloudCatalog struct {
	Tools []struct {
		Name      string `json:"name"`
		Arguments []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"arguments"`
	} `json:"tools"`
}

// TestSharedToolsMatchCloud is the "same name, same shape" rule as a test,
// against Cloud's tool catalog as it was last shipped (testdata).
//
// For every tool both servers have:
//   - every argument Cloud requires, this server takes, so a call written
//     against Cloud is a valid call here;
//   - every argument this server takes, Cloud declares too, or it is a
//     recorded decision in openOnlyArguments.
func TestSharedToolsMatchCloud(t *testing.T) {
	raw, err := os.ReadFile("testdata/cloud_tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var cloud cloudCatalog
	if err := json.Unmarshal(raw, &cloud); err != nil {
		t.Fatal(err)
	}
	cloudArgs := map[string]map[string]bool{} // tool -> argument -> required
	for _, tool := range cloud.Tools {
		args := map[string]bool{}
		for _, a := range tool.Arguments {
			args[a.Name] = a.Required
		}
		cloudArgs[tool.Name] = args
	}

	// The server with a runner, so the run tools are in the comparison.
	res, err := runSession(t, 100, false).s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	shared, used := 0, map[string]bool{}
	for _, tool := range res.Tools {
		theirs, ok := cloudArgs[tool.Name]
		if !ok {
			continue // open-core only; Cloud's catalog does not constrain it
		}
		shared++
		schema, _ := tool.InputSchema.(map[string]any)
		props, _ := schema["properties"].(map[string]any)

		for name, required := range theirs {
			if _, ok := props[name]; required && !ok {
				t.Errorf("%s: Cloud requires %q and this server does not take it, so a Cloud call fails here", tool.Name, name)
			}
		}
		var ours []string
		for name := range props {
			ours = append(ours, name)
		}
		sort.Strings(ours)
		for _, name := range ours {
			if _, ok := theirs[name]; ok {
				continue
			}
			key := tool.Name + "." + name
			if openOnlyArguments[key] == "" {
				t.Errorf("%s takes %q, which Cloud's %s does not. Add it to Cloud first and refresh "+
					"testdata/cloud_tools.json, or record why it is safe in openOnlyArguments", tool.Name, name, tool.Name)
				continue
			}
			used[key] = true
		}
	}
	if shared < 10 {
		t.Fatalf("only %d tools shared with Cloud; the catalog or the server did not load", shared)
	}
	// An entry for an argument that is no longer Open-only is stale: Cloud
	// caught up, or the argument went away.
	for key := range openOnlyArguments {
		if !used[key] {
			t.Errorf("openOnlyArguments[%q] no longer applies; remove it", key)
		}
	}
}

// TestTheRunToolsMatchCloudExactly: the tools that spend take exactly what
// Cloud's take, so nothing an agent sends here is refused or ignored there.
func TestTheRunToolsMatchCloudExactly(t *testing.T) {
	for key := range openOnlyArguments {
		for _, tool := range []string{"reevaluate_prompt.", "reevaluate_all_prompts."} {
			if strings.HasPrefix(key, tool) {
				t.Errorf("%s is Open-only; a run tool's arguments must exist on Cloud first", key)
			}
		}
	}
}
