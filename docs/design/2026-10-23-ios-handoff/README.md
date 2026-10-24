# Handoff: Field Assistant iOS (v1, RFI by dictation)

## Overview
iPhone app for Monroe Residential / 3F Construction field staff. The user signs in with Procore, picks a project, dictates an RFI with photos, reviews an LLM-prefilled form, and submits. Every later feature (Observation, Punch item, Daily log) reuses the same Capture → Extract → Review → Submit screens. See `procore-field-assistant-spec.md` and `docs/superpowers/specs/2026-10-22-v1-decisions.md` in the repo for product behaviour; this document covers the UI.

## About the design files
`Field Assistant iOS.dc.html` and `Field Assistant Logo Options.dc.html` are **design references built in HTML**. They are not production code. Recreate them in the existing SwiftUI project (`ios/FieldAssistant`, Swift 6, iOS 17+) using native controls and the project's `Core/Review` form components. Open the HTML in a browser (needs `support.js` and `ios-frame.jsx` beside it) to inspect exact values; everything is inline-styled.

## Fidelity
**High-fidelity.** Colors, type, spacing, copy and states are final. Match pixel values where SwiftUI allows; use native equivalents (Toggle, List rules, sheets) where shown.

## Brand
- Font: **Public Sans** (bundle 400/500/600/700 from Google Fonts). Kickers are uppercase, 11–13 px, weight 700, letter-spacing .12–.16em.
- Orange is reserved for the one action per screen (record, stop, submit, flagged field) and for small marks.
- Logo: waveform mark, 5 rounded bars, centre bar orange. Files in `logo/`.

## Design tokens
| Token | Value | Use |
| --- | --- | --- |
| brand orange | `#F26122` | record/stop/submit buttons on dark, ring, icons, flagged-field bar, logo bar |
| accent (AA on white) | `#C2410C` | link text, kickers, filled buttons on white, outline tags |
| accent tint | `#FEF0E8` | flagged-field background, button halo on white |
| charcoal | `#2F3437` | primary text; dark ground (sign-in, result, dark mode) |
| dark panel | `#3A4044` | raised surfaces on charcoal |
| muted text | `#5C6368` | secondary text on white |
| placeholder / tertiary | `#8A9196` | labels, chevrons, disabled text |
| light text on dark | `#B6BCC0` (secondary) / `#E9EBEC` (body) | |
| hairline | `#E1E4E6` light / `rgba(255,255,255,.12)` dark | row rules, card borders |
| surface | `#F4F5F6` | search field, neutral tags, disabled button |
| white | `#FFFFFF` | light ground |

Spacing: screen padding 20 px (28 px on sign-in/result), row padding 14 px vertical, section gap 20–24 px. Radii: buttons 6 px, cards 8 px, tags 4 px, sheet 18 px, circles for record/stop. Min tap height 44 px; primary buttons 54–56 px.

Type scale: screen title 32/700/-.02em; hero 56/700/.98 lh; result number 84/700/-.04em tabular; body 15/400/1.55; row title 15–17/500–600; label 11/700 uppercase .1em; meta 12–13/400; transcript 24/500/1.35.

## Screens
IDs match the badges in the HTML (`#1a` …).

**1a Sign in** — charcoal ground. Top: 44 px logo mark + "FIELD ASSISTANT" kicker (#B6BCC0). Centre: kicker "PROCORE, BY VOICE" (orange), "Field / Assistant" 56 px, 48×3 orange rule, 17 px grey tagline. Bottom: full-width orange button "Sign in with Procore" 56 px, then 12 px note. Error message (from `AuthError`) replaces the note in `#F26122`.

**1b Project picker** — white. Company switcher as an orange uppercase kicker with chevron (v1 has one company; render as a static label or hide chevron). Title "Choose a project". Search field 48 px on `#F4F5F6`, radius 8. Rows 72 px with hairlines: name 17/600, meta 13 muted, "LAST USED" tag (accent on tint), chevron. Projects where the user lacks RFI permission render in `#8A9196` with the reason, not tappable. Footer note about lookups prefetching.

**1c Home A (recommended)** — white. Header: company kicker, project name 28/700 with orange chevron (opens 1b), 44 px round settings button (hairline border). Feature grid 2×2, cards 88 px, radius 8: selected = 2 px orange border + "SELECTED" kicker; available = hairline; no permission = grey text; not shipped = dashed border "COMING". Record button 116 px orange circle, 10 px halo `#FEF0E8`, mic icon 46 px; label "Record an RFI" 17/700; sub 13 muted. "RECENT" list, rows 60 px: title 15/500 single-line ellipsis, meta 12 tabular, status tag right. Tags: Submitted = `#F4F5F6` fill "#214 Submitted"; Queued = orange outline; Draft = grey outline; Fix = orange fill white text.

**1d Home B (alternative)** — same chrome; segmented control (pill, selected = charcoal fill) replaces grid; 200 px record button with "RECORD" label. Not selected; kept for reference.

**1e Capture** — white. Top bar: "Cancel" (accent), blinking orange dot + "Recording 0:42" tabular, "RFI" kicker. Transcript fills the screen at 24/500 with an orange 3 px caret that blinks (1.1 s). Photo strip: 76 px squares radius 6 + dashed add button; header shows count and location/GPS. Bottom row over a hairline: 76 px orange stop button (8 px halo) + "Build RFI" 52 px button, disabled (`#F4F5F6` / `#8A9196`) until recording stops, then accent fill. Must work fully offline.

**1f Review** — white, scrolling. Back link "‹ Capture". Title "Review RFI" with tag "1 FIELD NEEDS YOU" (count of `unresolved[]`). Fields are stacked rows with 11 px uppercase labels and hairlines; tappable rows show a chevron and open a picker from `/lookups/{kind}`. Paired fields (Due/Drawing, Schedule/Cost impact) share a row split by a vertical hairline. **Unresolved field**: 4 px orange left bar, `#FEF0E8` background, radius 0 8 8 0, label in accent, reason text right ("Two people named Mike"), candidate radios 48 px rows with name 15/600 + role 12 muted, "Someone else…" link opens full picker. Photos 64 px. Collapsed "Field notes · original transcript" row. Sticky footer over hairline: "Save draft" (1.5 px charcoal outline) + "Submit to Procore" (`#C2410C` fill, flex 1.4). Procore 422 errors re-flag the row the same way with the server message as the reason.

**1g Result** — charcoal. 56 px orange check circle, kicker "CREATED IN PROCORE", "RFI 214" 84 px, subject 20/600, orange rule, definition list (Project, Assigned, Due, Attached, Sent + elapsed). Buttons: "Open in Procore ↗" orange fill, "New RFI" white outline, "Done" text link. Opens the deep link returned by the submit job.

**1h Settings** — white. Sections with kickers SUBMITTING / PROJECT / ACCOUNT, 64 px rows. Toggles: on = orange track white knob; off = `#E1E4E6` track. Photo size row shows "2,500 px". "Refresh people and locations" row with orange refresh icon. "Change" link to 1b. "Sign out" in accent. Footer: 28 px logo mark + version/build/API host 11 px.

**1i Reauth prompt** — bottom sheet over dimmed Home (`rgba(47,52,55,.55)`), radius 18 top, grabber. Lock icon in tint circle, title "Sign in to Procore again" 24/700, body explaining queued work is safe, a `#F4F5F6` strip listing queued drafts, "Sign in with Procore" accent button, "Not now" text. Shown when backend returns `reauth_required`.

**1j Offline / Drafts** — charcoal banner at top "No signal. Recording works; sending waits." Drafts list, 72 px rows with a third line of status copy: Waiting ("Needs connection to build the draft"), Queued ("Reviewed. Sends automatically, once."), Submitted, Fix (Procore field error, orange fill tag), Draft. Footer note on idempotency.

**1k–1m Dark mode** — ground `#2F3437`, text white, secondary `#B6BCC0`, hairlines `rgba(255,255,255,.12)`, raised surfaces `#3A4044`, links/kickers switch to `#F26122`, "Submit" uses `#F26122` fill, outline buttons use white. Flagged-field background becomes `#3A4044` with the orange bar.

## Interactions
- Navigation is a plain stack, no tab bar. Capture and Review are pushed; Reauth is a sheet; Settings pushes from Home.
- Record button → Capture starts recording immediately. Stop → "Build RFI" enables → `POST /drafts` → Review. No network: save as "Waiting" and return Home with the banner.
- Flagged radio selection clears the flag and decrements the title tag.
- Submit → sticky footer shows a spinner in the button, then Result. Failure with 422 → back to Review with rows flagged.
- Blink animations: recording dot and caret, 1.1 s, opacity 1→0→1, caret offset .3 s.
- Hit targets ≥ 44 px everywhere (gloves). VoiceOver labels on every control.

## State
`phase` (loading/signedOut/signedIn/offline/reauthRequired), selected company+project (persisted), feature permissions per project, recording state + live transcript, photo refs + GPS, draft (SwiftData) with status Draft/Waiting/Queued/Submitted/Failed, `unresolved[]` and `confidence` from `DraftResponse`, job status polling, settings (submit as Procore draft, photo size, keep audio), network reachability.

## Assets
`logo/` — waveform mark as SVG: `mark-on-dark`, `mark-on-light`, `mark-mono-white`, `mark-mono-charcoal`, `app-icon-charcoal`, `app-icon-white`, `lockup-on-dark`, `lockup-on-light`; PNG app icons `app-icon-1024.png`, `app-icon-180.png` (charcoal ground). Geometry: viewBox 44, bars at x = 8/15/22/29/36, heights 8/20/32/16/6 centred on y = 22, stroke 4 round. Icons in screens are Lucide (mic, square, camera, chevrons, settings, check, external-link, lock, wifi-off, refresh) at 1.75–2.5 stroke.

## Files
- `Field Assistant iOS.dc.html` — all 13 screens (open in a browser; canvas pans/zooms).
- `Field Assistant Logo Options.dc.html` — the six logo candidates; 1d was chosen.
- `ios-frame.jsx`, `support.js` — runtime needed to render the HTML; not design content.
- `logo/` — see Assets.
