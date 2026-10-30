-- Photos on a draft: the manifest row is created with the draft (pending),
-- the bytes land in the object store (stored), and the submit job pushes
-- them to Procore (uploaded) or records why it could not (failed).
create table attachments (
  id uuid primary key,
  company_id bigint not null,
  draft_id uuid not null references drafts(id) on delete cascade,
  filename text not null,
  content_type text not null,
  bytes bigint not null,
  object_key text,
  state text not null default 'pending',
  procore_upload_id text,
  uploaded_at timestamptz,
  error_code text,
  error_message text,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
create index attachments_draft_idx on attachments (draft_id, created_at, id);
