// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Command limelit is the whole product: the dashboard, the MCP server, the
// evaluation runner and the export, in one binary with no runtime
// dependencies.
//
//	limelit serve     UI + JSON API + MCP over HTTP + scheduler
//	limelit mcp       MCP over stdio, for a local Claude
//	limelit run       one evaluation pass, then exit
//	limelit export    JSON or CSV of everything
//	limelit upgrade   move to Limelit Cloud
//	limelit version   version and commit
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/limelit-co/open/internal/config"
	"github.com/limelit-co/open/internal/credentials"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/limelit-co/open/internal/export"
	"github.com/limelit-co/open/internal/httpx"
	"github.com/limelit-co/open/internal/mcpserver"
	"github.com/limelit-co/open/internal/metrics"
	"github.com/limelit-co/open/internal/provider"
	"github.com/limelit-co/open/internal/runner"
	"github.com/limelit-co/open/internal/secrets"
	"github.com/limelit-co/open/internal/store"
	"github.com/limelit-co/open/internal/target"
	"github.com/limelit-co/open/internal/ui"
	"github.com/limelit-co/open/internal/upgrade"
)

const usage = `limelit - self-hosted AI visibility tracking

Usage:
  limelit <command> [flags]

Commands:
  serve     run the dashboard, JSON API, MCP endpoint and scheduler
  mcp       run the MCP server over stdio (for Claude Desktop / Claude Code)
  run       run one evaluation pass and exit
  export    write everything this instance knows to stdout
  login     get a free Limelit Cloud key and save it here (no provider key needed)
  upgrade   move this instance to Limelit Cloud
  version   print version and build info

Run "limelit <command> -h" for the flags of one command.
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "limelit: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}

	// Ctrl-C and SIGTERM cancel the root context. Every command below either
	// returns promptly on cancellation or drains first; nothing is killed
	// mid-write.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return cmdServe(ctx, rest)
	case "mcp":
		return cmdMCP(ctx, rest)
	case "run":
		return cmdRun(ctx, rest)
	case "export":
		return cmdExport(ctx, rest)
	case "upgrade":
		return cmdUpgrade(ctx, rest)
	case "login":
		return cmdLogin(ctx, rest)
	case "version":
		fmt.Println(versionLine())
		return nil
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":1515", "listen address")
	cfgPath := fs.String("config", "limelit.yaml", "path to limelit.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log := newLogger()
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	registry := provider.Default()
	if cfg.Configured() {
		log.Info("configuration loaded",
			"property", cfg.Property.Name,
			"competitors", len(cfg.Competitors),
			"targets", len(cfg.Targets),
			"runs_per_day", cfg.Limits.RunsPerDay,
			"schedule", cfg.Schedule)
		// Targets are checked here, at startup, against the providers that
		// are actually compiled in. A typo belongs in the log on the line
		// after "configuration loaded", not three hours later in the middle
		// of a scheduled run. It is a warning rather than a fatal error
		// because the dashboard has to come up so the target can be fixed.
		if targets, err := target.ParseAll(cfg.Targets, registry); err != nil {
			log.Warn("some targets will not run", "error", err)
		} else {
			log.Info("targets resolved", "count", len(targets))
		}
	} else if _, err := db.Property(ctx); err != nil {
		// Not an error. A fresh instance boots into the setup wizard; that is
		// the whole point of the first-ten-minutes claim in the README. An
		// instance set up through the wizard has no file but has a property.
		log.Info("not configured yet, the setup wizard will run at the dashboard", "config", *cfgPath)
	}
	if len(registry.Names()) == 0 {
		log.Warn("no providers are compiled in yet, so no target can run")
	}

	keys, err := secrets.Open(config.DataDir())
	if err != nil {
		return err
	}

	// A pass can only be advanced by the process that started it, so one left
	// running by a crash would show as in flight forever.
	if n, err := db.SweepOrphanedEvaluations(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Warn("closed evaluations left running by an earlier process", "count", n)
	}

	run := runner.New(db, registry, credentials.Source(ctx, db, keys, log), log)
	run.NewAnalyzer = func(c context.Context) (runner.Analyzer, error) { return runner.NewStoreAnalyzer(c, db) }
	ver, rev := buildInfo()
	dash, err := ui.New(db, registry, keys, run, log, ver, rev, cfg)
	if err != nil {
		return err
	}

	startSchedule(ctx, dash, run, db, log)

	srv := httpx.New(*addr, db, log, ver, dash)
	err = srv.Serve(ctx, func(bound net.Addr) {
		log.Info("listening", "addr", bound.String(), "database", db.Path(), "version", ver, "commit", rev)
		// A public demo has no one at its terminal to read this.
		if ui.DemoMode() {
			return
		}
		setup, err := mcpserver.Status(ctx, db, metrics.New(db), baseURL(bound))
		if err != nil {
			log.Warn("could not read the setup state for the startup message", "error", err)
		}
		writeConnect(os.Stderr, connectInfo{
			version:   ver,
			addr:      bound,
			exe:       executablePath(),
			dataDir:   absPath(config.DataDir()),
			state:     setup.DataState,
			httpToken: dash.MCPToken() != "",
			container: containerID(),
		})
	})
	if err != nil {
		return err
	}
	log.Info("stopped")
	return nil
}

// startSchedule runs a pass on an interval, and re-reads the interval.
//
// Two intervals and off, nothing finer: the choice is really "how often",
// and two answers cover it without a syntax to learn. `limelit run` does one
// pass on demand.
//
// The mode comes from a function, not a value, because Settings can change
// it while the server runs. The loop wakes at least every schedulePoll to ask
// again, so daily to off or off to hourly takes effect within a minute and
// the screen never describes a schedule the process is not keeping.
//
// The first pass is due when the last one is older than the interval, or
// when none has ever run. A bare ticker would fire a full interval after the
// process started, so a host that restarts the process inside that interval
// (a container platform does, on every deploy) would never run at all; the
// demo sat on a month-old seed for exactly that reason. A short grace before
// the first pass lets the server come up first and keeps a restart loop from
// spending a run per restart.
func startSchedule(ctx context.Context, settings scheduleSettings, run *runner.Runner, db *store.DB, log *slog.Logger) {
	go func() {
		var lastAttempt time.Time
		announced := ""
		for {
			m := settings.ScheduleMode(ctx)
			every, ok := config.ScheduleInterval(m)
			if !ok {
				if announced != m {
					if config.ValidSchedule(m) {
						log.Info("automatic runs are off", "schedule", m)
					} else {
						log.Warn("unknown schedule, nothing will run automatically",
							"schedule", m, "supported", strings.Join(config.ScheduleModes, ", "))
					}
					announced = m
				}
				if !sleep(ctx, schedulePoll) {
					return
				}
				continue
			}

			counts, err := db.Counts(ctx)
			wait := nextDue(counts.LastChatAt, err, lastAttempt, every, scheduleGrace, time.Now())
			if announced != m {
				log.Info("scheduling automatic runs", "every", every, "next", wait.Round(time.Second))
				announced = m
			}
			if wait > schedulePoll {
				// Not due yet. Sleep a poll and ask the setting again rather
				// than committing to a wait the user may change.
				if !sleep(ctx, schedulePoll) {
					return
				}
				continue
			}
			if !sleep(ctx, wait) {
				return
			}
			lastAttempt = time.Now()

			res, err := run.Run(ctx, runner.Options{RunsPerDay: settings.RunsPerDay(ctx)})
			if err != nil {
				log.Error("scheduled run failed", "error", err)
			} else {
				log.Info("scheduled run finished", "evaluation", res.EvaluationID,
					"completed", res.Completed, "failed", res.Failed)
			}
		}
	}()
}

// scheduleSettings is what the scheduler reads on every wake: the cadence
// and the ceiling, each resolved Settings first, then limelit.yaml, then the
// default. The dashboard implements it, so the two agree by construction.
type scheduleSettings interface {
	ScheduleMode(context.Context) string
	RunsPerDay(context.Context) int
}

// sleep waits d or until ctx ends, reporting whether to carry on.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// scheduleGrace is how long the first scheduled pass waits after start.
const scheduleGrace = 30 * time.Second

// schedulePoll is how often the scheduler re-reads the schedule setting.
const schedulePoll = time.Minute

// nextDue is how long until the next pass: firstDue's answer, pushed out so
// that at least one interval separates two attempts by this process. Without
// the second rule a pass that recorded nothing (nothing to run, every call
// failed before a row was written) would leave the last answer old, firstDue
// would say "due now" again, and the loop would retry every grace period.
func nextDue(lastChatAt string, lookup error, lastAttempt time.Time, every, grace time.Duration, now time.Time) time.Duration {
	wait := firstDue(lastChatAt, lookup, every, grace, now)
	if !lastAttempt.IsZero() {
		if w := lastAttempt.Add(every).Sub(now); w > wait {
			wait = w
		}
	}
	return wait
}

// firstDue is how long to wait before the first scheduled pass: the rest of
// the interval since the last answer was recorded, the grace when that has
// already elapsed or nothing has run, and the full interval when the store
// cannot be read (an error should not trigger a run).
//
// The last answer's time, not the last evaluation's: an import of history
// writes the answers with the days they were measured and the evaluation
// rows with the day of the import, and it is the measurement that says
// whether today is covered.
func firstDue(lastChatAt string, lookup error, every, grace time.Duration, now time.Time) time.Duration {
	if lookup != nil {
		return every
	}
	if lastChatAt == "" {
		return grace
	}
	last, err := time.Parse("2006-01-02 15:04:05", lastChatAt)
	if err != nil {
		return every
	}
	wait := last.Add(every).Sub(now)
	if wait < grace {
		return grace
	}
	return wait
}

// cmdMCP serves the tool catalog over stdio, which is the shape Claude
// Desktop and most MCP clients launch.
//
// Nothing is written to stdout except protocol frames: stdout IS the
// transport, so a stray Println would corrupt the session. Logs go to stderr.
func cmdMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	srv, err := mcpserver.New(mcpserver.Deps{DB: db})
	if err != nil {
		return err
	}
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// cmdRun executes one evaluation pass and exits: a manual check, or one
// target on its own.
func cmdRun(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	targetSpec := fs.String("target", "", "run only this target (engine:provider[:model][:online])")
	cfgPath := fs.String("config", "limelit.yaml", "path to limelit.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	log := newLogger()
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	keys, err := secrets.Open(config.DataDir())
	if err != nil {
		return err
	}
	if _, err := db.SweepOrphanedEvaluations(ctx); err != nil {
		return err
	}

	ceiling := cfg.Limits.RunsPerDay
	if stored, err := db.Setting(ctx, "runs_per_day"); err == nil && stored != "" {
		if n, err := strconv.Atoi(stored); err == nil && n > 0 {
			ceiling = n
		}
	}

	run := runner.New(db, provider.Default(), credentials.Source(ctx, db, keys, log), log)
	run.NewAnalyzer = func(c context.Context) (runner.Analyzer, error) { return runner.NewStoreAnalyzer(c, db) }
	res, err := run.Run(ctx, runner.Options{TargetSpec: *targetSpec, RunsPerDay: ceiling})
	if err != nil {
		return err
	}
	fmt.Printf("evaluation %d: %d of %d answers, %d failed, in %s\n",
		res.EvaluationID, res.Completed, res.Planned, res.Failed, res.Duration.Round(time.Second))
	if res.Failed > 0 {
		// A pass whose provider key expired should not report success.
		return fmt.Errorf("%d of %d answers failed", res.Failed, res.Planned)
	}
	return nil
}

// cmdExport will write the payload that `upgrade` also sends.
func cmdExport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	format := fs.String("format", "json", "json or csv")
	out := fs.String("out", "", "write here instead of stdout; for csv this is a directory (default limelit-export)")
	since := fs.String("since", "", "only answers created on or after this date (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	opts := export.Options{Since: *since, Now: time.Now().UTC().Format(time.RFC3339)}

	switch strings.ToLower(*format) {
	case "json":
		w := io.Writer(os.Stdout)
		if *out != "" {
			f, err := os.Create(*out)
			if err != nil {
				return err
			}
			defer f.Close()
			w = f
		}
		if err := export.WriteJSON(ctx, db, w, opts); err != nil {
			return err
		}
		if *out != "" {
			fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
		}
		return nil

	case "csv":
		dir := *out
		if dir == "" {
			dir = "limelit-export"
		}
		files, err := export.WriteCSV(ctx, db, dir, opts)
		if err != nil {
			return err
		}
		for _, f := range files {
			fmt.Fprintln(os.Stderr, "wrote", f)
		}
		return nil

	default:
		return fmt.Errorf("format must be json or csv, not %q", *format)
	}
}

func cmdUpgrade(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	key := fs.String("key", "", "Limelit Cloud API key (or set "+upgrade.KeyEnv+")")
	endpoint := fs.String("endpoint", "", "Cloud import endpoint (or set "+upgrade.EndpointEnv+")")
	since := fs.String("since", "", "only send answers created on or after this date (YYYY-MM-DD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := store.Open(ctx, config.DatabasePath())
	if err != nil {
		return err
	}
	defer db.Close()

	opts := upgrade.Options{
		Key:      firstNonEmpty(*key, os.Getenv(upgrade.KeyEnv)),
		Endpoint: firstNonEmpty(*endpoint, os.Getenv(upgrade.EndpointEnv)),
		Since:    *since,
	}

	fmt.Fprintln(os.Stderr, "Exporting and uploading to Limelit Cloud. Nothing here is deleted.")
	result, err := upgrade.Run(ctx, db, opts)
	if err != nil {
		return err
	}

	fmt.Printf("Imported into Limelit Cloud:\n")
	fmt.Printf("  %d prompts, %d competitors, %d answers\n",
		result.Import.Prompts, result.Import.Competitors, result.Import.Chats)
	fmt.Printf("  %d mentions, %d citations\n", result.Import.Mentions, result.Import.Citations)
	if result.Import.ChatsSkipped > 0 {
		fmt.Printf("  %d answers were already there from an earlier run and were left alone\n", result.Import.ChatsSkipped)
	}
	if result.WorkspaceURL != "" {
		fmt.Printf("\nWorkspace: %s\n", result.WorkspaceURL)
	}
	if result.MCPURL != "" {
		fmt.Printf("MCP endpoint: %s\n", result.MCPURL)
		fmt.Printf("\nPoint your MCP client there with the same API key:\n")
		fmt.Printf(`  {"mcpServers":{"limelit":{"url":%q,"headers":{"Authorization":"Bearer <your key>"}}}}`+"\n", result.MCPURL)
	}
	fmt.Fprintln(os.Stderr, "\nThis instance still has everything. Re-running is safe.")
	return nil
}

// firstNonEmpty returns the first value that is set, so a flag beats the
// environment and neither silently wins over the other.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if os.Getenv("LIMELIT_DEBUG") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
