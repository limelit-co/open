-- A heartbeat on each pass, so a second process can tell a live pass from
-- one whose process died.
--
-- `limelit serve`, `limelit run` and `limelit mcp` are separate processes on
-- this one file. The runner's own lock only works inside one of them, and
-- without this column the only way to close a pass left by a crash was to
-- close every running pass at startup, including one another process was in
-- the middle of. The runner touches heartbeat_at while it works; a running
-- row whose heartbeat is fresh is live, and one whose heartbeat is stale (or
-- NULL, which is every row written before this column and every row not
-- written by the runner) belongs to a process that is gone.
ALTER TABLE evaluation ADD COLUMN heartbeat_at TEXT;

-- error says why a pass stopped before it fetched anything (a provider with
-- no key, an analyzer that could not load), so a status read from another
-- process, or by an agent, can say what to fix rather than only "failed".
ALTER TABLE evaluation ADD COLUMN error TEXT;
