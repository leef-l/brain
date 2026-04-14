CREATE TABLE IF NOT EXISTS signal_traces (
    id                  BIGSERIAL PRIMARY KEY,
    trace_id            VARCHAR(64) UNIQUE NOT NULL,
    symbol              VARCHAR(32) NOT NULL,
    snapshot_seq        BIGINT NOT NULL,
    outcome             VARCHAR(32),
    price               DOUBLE PRECISION,
    direction           VARCHAR(8),
    confidence          DOUBLE PRECISION,
    dominant_strategy   VARCHAR(32),
    global_risk_allowed BOOLEAN,
    global_risk_reason  TEXT,
    review_requested    BOOLEAN DEFAULT FALSE,
    review_approved     BOOLEAN,
    review_reason       TEXT,
    review_size_factor  DOUBLE PRECISION,
    rejected_stage      VARCHAR(32),
    reason              TEXT,
    draft_candidates    JSONB,
    account_results     JSONB,
    signals_json        JSONB,
    created_at          TIMESTAMPTZ DEFAULT NOW(),
    updated_at          TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_signal_traces_created_at
    ON signal_traces (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_signal_traces_symbol_created_at
    ON signal_traces (symbol, created_at DESC);
