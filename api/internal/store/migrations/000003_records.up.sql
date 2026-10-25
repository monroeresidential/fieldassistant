create sequence records_change_seq;

create table user_records (
  company_id bigint not null,
  user_id uuid not null references users(id) on delete cascade,
  project_id bigint not null,
  kind text not null,
  procore_id bigint not null,
  procore_updated_at timestamptz not null,
  body jsonb not null,
  body_hash text not null,
  deleted_at timestamptz,
  change_seq bigint not null,
  synced_at timestamptz not null default now(),
  primary key (user_id, project_id, kind, procore_id)
);
create index user_records_delta on user_records (user_id, project_id, kind, change_seq);

create table user_sync_state (
  company_id bigint not null,
  user_id uuid not null references users(id) on delete cascade,
  project_id bigint not null,
  kind text not null,
  high_water timestamptz,
  last_pull_at timestamptz,
  last_sweep_at timestamptz,
  last_requested_at timestamptz not null default now(),
  last_error text,
  primary key (user_id, project_id, kind)
);
