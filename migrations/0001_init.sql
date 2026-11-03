CREATE TABLE tenants (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Unique across all tenants, including destroyed ones: tenants are never
    -- hard-deleted, so a slug can never be reused.
    slug       text        NOT NULL CONSTRAINT tenants_slug_key UNIQUE,
    name       text        NOT NULL CHECK (btrim(name) <> ''),
    status     text        NOT NULL CHECK (status IN
                   ('provisioning', 'active', 'updating', 'destroying', 'destroyed', 'failed')),
    version    bigint      NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tenants_created_at_id_idx ON tenants (created_at, id);
CREATE INDEX tenants_status_created_at_id_idx ON tenants (status, created_at, id);

CREATE TABLE tasks (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    type       text        NOT NULL CHECK (type IN ('deploy', 'update', 'destroy')),
    tenant_id  uuid        NOT NULL REFERENCES tenants (id),
    status     text        NOT NULL CHECK (status IN ('accepted', 'in_progress', 'done', 'failed')),
    error      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tasks_created_at_id_idx ON tasks (created_at, id);
CREATE INDEX tasks_tenant_id_created_at_id_idx ON tasks (tenant_id, created_at, id);

-- Safety net: the tenant status guards already allow only one open task per
-- tenant, this makes a bug there fail loudly instead of silently.
CREATE UNIQUE INDEX tasks_one_open_per_tenant ON tasks (tenant_id)
    WHERE status IN ('accepted', 'in_progress');

CREATE TABLE outbox (
    id         bigserial   PRIMARY KEY,
    task_id    uuid        NOT NULL REFERENCES tasks (id),
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);

CREATE INDEX outbox_unsent_idx ON outbox (id) WHERE sent_at IS NULL;

CREATE TABLE processed_messages (
    message_id   text        PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);
