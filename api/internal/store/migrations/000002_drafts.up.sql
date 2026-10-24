create table lookup_cache (
  company_id bigint not null,
  project_id bigint not null,
  kind text not null,
  etag text not null,
  body jsonb not null,
  unavailable boolean not null default false,
  reason text,
  fetched_at timestamptz not null default now(),
  primary key (company_id, project_id, kind)
);

create table drafts (
  id uuid primary key,
  company_id bigint not null,
  project_id bigint not null,
  user_id uuid not null references users(id) on delete cascade,
  feature text not null,
  status text not null,
  transcript text not null,
  captured_at timestamptz not null,
  llm_output jsonb,
  user_payload jsonb,
  unresolved jsonb not null default '[]'::jsonb,
  confidence jsonb not null default '{}'::jsonb,
  model_version text,
  prompt_version text,
  extract_ms integer,
  procore_record_id bigint,
  procore_record_number text,
  procore_url text,
  error_code text,
  error_message text,
  job_id bigint,
  -- submit_attempts counts the submit jobs queued for the draft; each job's
  -- unique key includes its number.
  submit_attempts integer not null default 0,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index drafts_user_project_idx on drafts (user_id, project_id, created_at desc);
-- One Procore record belongs to at most one draft, so two drafts recovering
-- at once cannot both adopt it.
create unique index drafts_procore_record_uidx on drafts (company_id, procore_record_id) where procore_record_id is not null;
create index drafts_user_job_idx on drafts (user_id, job_id) where job_id is not null;

create table audit_events (
  id bigserial primary key,
  company_id bigint not null,
  draft_id uuid not null references drafts(id) on delete cascade,
  kind text not null,
  payload jsonb,
  procore_request jsonb,
  procore_response jsonb,
  duration_ms integer,
  created_at timestamptz not null default now()
);
create index audit_events_draft_idx on audit_events (draft_id, created_at);
