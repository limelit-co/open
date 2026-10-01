// Copyright 2026 Limelit. Licensed under the Apache License, Version 2.0.
// See the LICENSE file in the repository root for the full terms.

// Package config loads the instance configuration: limelit.yaml for what to
// track, environment variables for credentials.
//
// The two are deliberately separate. limelit.yaml is meant to be committed
// and diffed, so it holds prompts, competitors and targets and never a key.
// Credentials come from the environment or, later, the settings store; the
// environment always wins so a deployment can override a stored value
// without editing the database.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultRunsPerDay caps how many single answers one instance will fetch in a
// day. It is a ceiling on surprise, not a budget: this project never converts
// usage to currency.
const DefaultRunsPerDay = 200

// RunsPerDaySetting is the Settings key that overrides limits.runs_per_day.
const RunsPerDaySetting = "runs_per_day"

// ResolveRunsPerDay is the daily ceiling every entry point applies: the value
// saved in Settings, then limelit.yaml, then DefaultRunsPerDay. stored is the
// Settings value, "" when none is saved.
func ResolveRunsPerDay(stored string, cfg *Config) int {
	if n, err := strconv.Atoi(strings.TrimSpace(stored)); err == nil && n > 0 {
		return n
	}
	if cfg != nil && cfg.Limits.RunsPerDay > 0 {
		return cfg.Limits.RunsPerDay
	}
	return DefaultRunsPerDay
}

// Config is the parsed limelit.yaml.
type Config struct {
	Property    Property     `yaml:"property"`
	Competitors []Competitor `yaml:"competitors"`
	Targets     []string     `yaml:"targets"`
	Limits      Limits       `yaml:"limits"`
	Schedule    string       `yaml:"schedule"`
}

// Property is the brand this instance tracks.
type Property struct {
	Name    string   `yaml:"name"`
	Domain  string   `yaml:"domain"`
	Aliases []string `yaml:"aliases"`
}

// Competitor is one tracked rival. Domain is the identity key, as it is in
// Limelit Cloud: two rows for the same competitor spelled differently would
// otherwise collide with citation-based domain tracking.
type Competitor struct {
	Name     string `yaml:"name"`
	Domain   string `yaml:"domain"`
	Category string `yaml:"category"`
}

// Limits holds the run guard.
type Limits struct {
	RunsPerDay int `yaml:"runs_per_day"`
}

// Load reads a config file. A missing file is not an error: a fresh instance
// is configured through the setup wizard, and the zero Config is what the
// wizard starts from.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults(&Config{}), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return defaults(&cfg), nil
}

func defaults(c *Config) *Config {
	if c.Limits.RunsPerDay <= 0 {
		c.Limits.RunsPerDay = DefaultRunsPerDay
	}
	if c.Schedule == "" {
		c.Schedule = "off"
	}
	return c
}

// Validate reports what is wrong with a config a user meant to run. It is
// separate from Load because an unconfigured instance is a valid state: it
// boots into the wizard rather than refusing to start.
func (c *Config) Validate() error {
	var problems []string
	if strings.TrimSpace(c.Property.Name) == "" {
		problems = append(problems, "property.name is empty")
	}
	if strings.TrimSpace(c.Property.Domain) == "" {
		problems = append(problems, "property.domain is empty")
	}
	for i, comp := range c.Competitors {
		if strings.TrimSpace(comp.Domain) == "" {
			problems = append(problems, fmt.Sprintf("competitors[%d] (%s) has no domain, which is its identity key", i, comp.Name))
		}
	}
	if len(c.Targets) == 0 {
		problems = append(problems, "targets is empty, so nothing would be asked")
	}
	if len(problems) > 0 {
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Configured reports whether this instance has been set up. An unconfigured
// instance serves the wizard and nothing else.
func (c *Config) Configured() bool { return c.Validate() == nil }

// DataDirEnv overrides where the database lives.
const DataDirEnv = "LIMELIT_DATA_DIR"

// DataDir is where the database lives. LIMELIT_DATA_DIR overrides it.
func DataDir() string {
	if d := strings.TrimSpace(os.Getenv(DataDirEnv)); d != "" {
		return d
	}
	return "data"
}

// DatabasePath is the SQLite file inside DataDir.
func DatabasePath() string { return filepath.Join(DataDir(), "limelit.db") }

// Credential returns a provider credential from the environment. The settings
// store is consulted by the caller only when this returns "", so an exported
// variable always wins over a stored value.
func Credential(name string) string { return strings.TrimSpace(os.Getenv(name)) }

// Schedule modes. Two intervals and off, nothing finer: the choice is really
// "how often", and two answers cover it without a syntax to learn.
const (
	ScheduleOff    = "off"
	ScheduleDaily  = "daily"
	ScheduleHourly = "hourly"
)

// ScheduleModes lists the accepted values, in the order a form shows them.
var ScheduleModes = []string{ScheduleDaily, ScheduleHourly, ScheduleOff}

// ScheduleInterval turns a schedule mode into how often a pass runs. ok is
// false for off and for anything unrecognised, which the caller treats the
// same way: nothing runs on its own.
func ScheduleInterval(mode string) (every time.Duration, ok bool) {
	switch NormalizeSchedule(mode) {
	case ScheduleDaily:
		return 24 * time.Hour, true
	case ScheduleHourly:
		return time.Hour, true
	}
	return 0, false
}

// NormalizeSchedule lower-cases and trims a mode, mapping "" to off.
func NormalizeSchedule(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return ScheduleOff
	}
	return m
}

// ValidSchedule reports whether mode is one this binary accepts.
func ValidSchedule(mode string) bool {
	m := NormalizeSchedule(mode)
	for _, known := range ScheduleModes {
		if m == known {
			return true
		}
	}
	return false
}
