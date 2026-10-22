create table users (
  id uuid primary key default gen_random_uuid(),
  procore_user_id bigint not null unique,
  email text not null,
  name text not null,
  reauth_required boolean not null default false,
  last_company_id bigint,
  last_project_id bigint,
  created_at timestamptz not null default now()
);
create table procore_tokens (
  user_id uuid primary key references users(id) on delete cascade,
  refresh_token_enc bytea not null,
  access_token_enc bytea not null,
  expires_at timestamptz not null,
  updated_at timestamptz not null default now()
);
create table sessions (
  id uuid primary key default gen_random_uuid(),
  user_id uuid not null references users(id) on delete cascade,
  device_id text not null,
  refresh_hash bytea not null,
  prev_refresh_hash bytea,
  expires_at timestamptz not null,
  created_at timestamptz not null default now()
);
create index sessions_user_id_idx on sessions(user_id);
create unique index sessions_refresh_hash_idx on sessions(refresh_hash);
create table auth_states (
  state text primary key,
  device_id text not null,
  code_challenge text not null,
  expires_at timestamptz not null
);
create table auth_codes (
  code text primary key,
  session_id uuid not null references sessions(id) on delete cascade,
  code_challenge text not null,
  expires_at timestamptz not null
);
