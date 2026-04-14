CREATE TABLE IF NOT EXISTS account_snapshots (
    id           BIGSERIAL PRIMARY KEY,
    account_id   VARCHAR(32) NOT NULL,
    date         DATE NOT NULL,
    equity       DOUBLE PRECISION NOT NULL,
    pnl_day      DOUBLE PRECISION,
    pnl_pct      DOUBLE PRECISION,
    trades       INT DEFAULT 0,
    wins         INT DEFAULT 0,
    max_dd       DOUBLE PRECISION,
    sharpe       DOUBLE PRECISION,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (account_id, date)
);

CREATE INDEX IF NOT EXISTS idx_account_snapshots_account_date
    ON account_snapshots (account_id, date DESC);

CREATE TABLE IF NOT EXISTS daily_reviews (
    id                 BIGSERIAL PRIMARY KEY,
    date               DATE UNIQUE NOT NULL,
    input_json         JSONB NOT NULL,
    analysis_json      JSONB NOT NULL,
    actions_json       JSONB,
    actions_executed   BOOLEAN DEFAULT FALSE,
    review_duration_ms INT,
    created_at         TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_daily_reviews_date
    ON daily_reviews (date DESC);
