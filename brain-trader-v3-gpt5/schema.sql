-- brain-trader-v3-gpt5 PostgreSQL schema
-- Schema name: trader

CREATE SCHEMA IF NOT EXISTS trader;
SET search_path TO trader;

CREATE TABLE IF NOT EXISTS candles (
    inst_id   VARCHAR(32) NOT NULL,
    bar       VARCHAR(8)  NOT NULL,
    ts        BIGINT      NOT NULL,
    o         DOUBLE PRECISION NOT NULL,
    h         DOUBLE PRECISION NOT NULL,
    l         DOUBLE PRECISION NOT NULL,
    c         DOUBLE PRECISION NOT NULL,
    vol       DOUBLE PRECISION NOT NULL,
    vol_ccy   DOUBLE PRECISION,
    PRIMARY KEY (inst_id, bar, ts)
);

CREATE INDEX IF NOT EXISTS idx_candles_ts ON candles (bar, ts DESC);

CREATE TABLE IF NOT EXISTS vectors (
    id         BIGSERIAL PRIMARY KEY,
    collection VARCHAR(32) NOT NULL,
    inst_id    VARCHAR(32) NOT NULL,
    timeframe  VARCHAR(8),
    ts         BIGINT NOT NULL,
    vector     BYTEA NOT NULL,
    metadata   JSONB,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_vectors_lookup
    ON vectors (collection, inst_id, timeframe, ts DESC);

CREATE TABLE IF NOT EXISTS vector_labels (
    vector_id     BIGINT PRIMARY KEY REFERENCES vectors(id) ON DELETE CASCADE,
    ret_5m        DOUBLE PRECISION,
    ret_15m       DOUBLE PRECISION,
    ret_1h        DOUBLE PRECISION,
    ret_4h        DOUBLE PRECISION,
    ret_24h       DOUBLE PRECISION,
    max_up_24h    DOUBLE PRECISION,
    max_down_24h  DOUBLE PRECISION,
    labeled_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS trades (
    id              BIGSERIAL PRIMARY KEY,
    mode            VARCHAR(8) NOT NULL,
    inst_id         VARCHAR(32) NOT NULL,
    direction       VARCHAR(8) NOT NULL,
    leverage        INT NOT NULL,
    entry_price     DOUBLE PRECISION NOT NULL,
    exit_price      DOUBLE PRECISION,
    quantity        VARCHAR(32) NOT NULL,
    pnl_pct         DOUBLE PRECISION,
    pnl_usdt        DOUBLE PRECISION,
    hold_seconds    INT,
    strategy        VARCHAR(32) NOT NULL,
    entry_reason    TEXT,
    exit_reason     VARCHAR(32),
    max_profit_pct  DOUBLE PRECISION,
    max_loss_pct    DOUBLE PRECISION,
    entry_vector_id BIGINT REFERENCES vectors(id),
    opened_at       TIMESTAMPTZ NOT NULL,
    closed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_trades_time ON trades (opened_at DESC);
CREATE INDEX IF NOT EXISTS idx_trades_strategy ON trades (strategy, opened_at DESC);

CREATE TABLE IF NOT EXISTS strategy_daily (
    strategy  VARCHAR(32) NOT NULL,
    date      DATE NOT NULL,
    mode      VARCHAR(8) NOT NULL,
    trades    INT NOT NULL DEFAULT 0,
    wins      INT NOT NULL DEFAULT 0,
    pnl_pct   DOUBLE PRECISION DEFAULT 0,
    sharpe    DOUBLE PRECISION,
    max_dd    DOUBLE PRECISION,
    weight    DOUBLE PRECISION,
    PRIMARY KEY (strategy, date, mode)
);

CREATE TABLE IF NOT EXISTS system_state (
    key         VARCHAR(64) PRIMARY KEY,
    value       JSONB NOT NULL,
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);
