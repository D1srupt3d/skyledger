-- skyledger schema v1. Applied once, in a transaction, by `skyledger migrate`.
-- Runtime settings that come from the environment (live retention, the
-- grafana role's password, the time zone) are applied by migrate.py after
-- this, on every run, so changing .env takes effect on the next start.

CREATE EXTENSION IF NOT EXISTS timescaledb;

-- Live positions from aircraft.json: one row per fresh position fix.
CREATE TABLE positions (
    time             timestamptz NOT NULL,
    hex              text NOT NULL,
    flight           text,
    squawk           text,
    source           text,
    lat              double precision NOT NULL,
    lon              double precision NOT NULL,
    alt_baro         integer,
    on_ground        boolean NOT NULL,
    alt_geom         integer,
    gs               real,
    track            real,
    baro_rate        integer,
    distance_nm      real,
    bearing          real,
    rssi             real,
    emergency        text,
    wind_dir         smallint,
    wind_speed       smallint,
    oat              smallint,
    tat              smallint,
    ias              smallint,
    tas              smallint,
    mach             real,
    roll             real,
    mag_heading      real,
    true_heading     real,
    track_rate       real,
    geom_rate        integer,
    nav_altitude_mcp integer,
    nav_altitude_fms integer,
    nav_heading      real,
    nav_qnh          real,
    nav_modes        text[],
    nic              smallint,
    nac_p            smallint
);
SELECT create_hypertable('positions', by_range('time', INTERVAL '1 day'));
CREATE INDEX positions_hex_time_idx ON positions (hex, time DESC);
ALTER TABLE positions SET (timescaledb.enable_columnstore = true,
                           timescaledb.segmentby = 'hex', timescaledb.orderby = 'time DESC');
CALL add_columnstore_policy('positions', after => INTERVAL '7 days');

-- One row per airframe seen live, with readsb's database fields.
-- db_flags bits: 1 military, 2 interesting, 4 PIA, 8 LADD.
CREATE TABLE aircraft (
    hex          text PRIMARY KEY,
    registration text,
    type_code    text,
    description  text,
    operator     text,
    year         text,
    category     text,
    db_flags     smallint NOT NULL DEFAULT 0,
    first_seen   timestamptz NOT NULL,
    last_seen    timestamptz NOT NULL
);

-- readsb's stats.json, one row per 1-minute window. Signal fields are NULL
-- on a readsb without its own SDR (network input only).
CREATE TABLE receiver_stats (
    time                 timestamptz NOT NULL,
    window_seconds       real,
    signal               real,
    noise                real,
    peak_signal          real,
    strong_signals       integer,
    messages             integer,
    messages_valid       integer,
    positions            integer,
    max_distance_m       real,
    aircraft_with_pos    integer,
    aircraft_without_pos integer,
    gain_db              real,
    ppm                  real,
    samples_dropped      bigint,
    samples_lost         bigint
);
-- No default time index: the unique one below covers it, and create_hypertable's default
-- would be named receiver_stats_time_idx too.
SELECT create_hypertable('receiver_stats', by_range('time', INTERVAL '7 days'), create_default_indexes => false);
CREATE UNIQUE INDEX receiver_stats_time_key ON receiver_stats (time);
ALTER TABLE receiver_stats SET (timescaledb.enable_columnstore = true, timescaledb.orderby = 'time DESC');
CALL add_columnstore_policy('receiver_stats', after => INTERVAL '7 days');
SELECT add_retention_policy('receiver_stats', drop_after => INTERVAL '365 days');

-- readsb's heatmap replay history at one sample per aircraft per minute. Kept forever.
CREATE TABLE history_positions (
    time        timestamptz NOT NULL,
    hex         text NOT NULL,
    flight      text,
    squawk      text,
    source      text,
    lat         double precision NOT NULL,
    lon         double precision NOT NULL,
    alt_baro    integer,
    on_ground   boolean NOT NULL,
    gs          real,
    distance_nm real,
    bearing     real
);
SELECT create_hypertable('history_positions', by_range('time', INTERVAL '7 days'));
CREATE INDEX history_positions_hex_time_idx ON history_positions (hex, time DESC);
ALTER TABLE history_positions SET (timescaledb.enable_columnstore = true,
                                   timescaledb.segmentby = 'hex', timescaledb.orderby = 'time DESC');
CALL add_columnstore_policy('history_positions', after => INTERVAL '7 days');

-- One row per heatmap file ever attempted, written in the same transaction as
-- its positions, so each file is imported exactly once.
CREATE TABLE history_files (
    path        text PRIMARY KEY,
    rows        integer NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now()
);

-- Per local day (TZ), refreshed by the importer. A plain table, not a
-- continuous aggregate: those can't count(DISTINCT ...).
CREATE TABLE daily_stats (
    day                   date PRIMARY KEY,
    aircraft              integer NOT NULL,
    positions             integer NOT NULL,
    max_range_nm          real,
    busiest_hour          smallint,
    busiest_hour_aircraft integer
);

-- tar1090's aircraft database, reloaded when its version changes.
CREATE TABLE aircraft_db (
    hex          text PRIMARY KEY,
    registration text,
    type_code    text,
    description  text,
    flags        smallint NOT NULL DEFAULT 0
);
CREATE TABLE airlines (
    code     text PRIMARY KEY,
    name     text,
    country  text,
    callsign text
);
CREATE TABLE db_meta (
    key   text PRIMARY KEY,
    value text NOT NULL
);

-- All-time reception range: furthest corroborated position per degree of bearing.
CREATE TABLE history_outline (
    deg         smallint PRIMARY KEY,
    lat         double precision NOT NULL,
    lon         double precision NOT NULL,
    distance_nm real NOT NULL,
    alt_baro    integer,
    hex         text NOT NULL,
    time        timestamptz NOT NULL
);

-- One row per flight: an aircraft's history split where it went unheard for
-- more than 30 minutes; single-position segments dropped.
CREATE MATERIALIZED VIEW flights AS
WITH marked AS (
    SELECT hex, time, flight, squawk, alt_baro, on_ground, distance_nm, bearing,
           CASE WHEN lag(time) OVER w IS NULL OR time - lag(time) OVER w > interval '30 minutes'
                THEN 1 ELSE 0 END AS starts_flight
    FROM history_positions
    WINDOW w AS (PARTITION BY hex ORDER BY time)
), numbered AS (
    SELECT *, sum(starts_flight) OVER (PARTITION BY hex ORDER BY time) AS seq
    FROM marked
)
SELECT hex,
       seq,
       mode() WITHIN GROUP (ORDER BY flight) AS callsign,
       mode() WITHIN GROUP (ORDER BY squawk) AS squawk,
       min(time) AS first_seen,
       max(time) AS last_seen,
       count(*) AS positions,
       min(alt_baro) AS min_alt,
       max(alt_baro) AS max_alt,
       min(distance_nm) AS closest_nm,
       max(distance_nm) AS furthest_nm,
       (array_agg(bearing ORDER BY time))[1] AS entry_bearing,
       (array_agg(bearing ORDER BY time DESC))[1] AS exit_bearing,
       bool_or(on_ground) AS seen_on_ground
FROM numbered
GROUP BY hex, seq
HAVING count(*) >= 2
WITH NO DATA;
CREATE UNIQUE INDEX flights_hex_first_seen_idx ON flights (hex, first_seen);
CREATE INDEX flights_first_seen_idx ON flights (first_seen);

-- Grafana's read-only login; migrate.py enables LOGIN and sets the password
-- from GRAFANA_DB_PASSWORD. On a shared server the platform creates it first,
-- so this needs no CREATEROLE there. Table grants are in 0002.
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'skyledger_grafana') THEN
        CREATE ROLE skyledger_grafana NOLOGIN;
    END IF;
END $$;
