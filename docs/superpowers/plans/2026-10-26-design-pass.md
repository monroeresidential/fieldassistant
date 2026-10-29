# Design Pass (roadmap 2b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every iOS screen matches the design handoff (tokens, Public Sans, logo, states, light and dark), and the RFI list and detail, which the handoff does not draw, use the same language.

**Architecture:** A design layer in `ios/FieldAssistant/Core/Design/` holds the tokens (adaptive colours, Public Sans type scale), the logo and app icon assets, and the shared pieces every screen is built from (kicker, status tag, buttons, form row, search field, hairline, record button, offline banner). Each screen task then rebuilds one or two screens on that layer, keeping their view models, routes and tests. Behaviour does not change; the reauth sheet (handoff 1i) needs an auth-state change the app does not have and is deferred to milestone 4.

**Tech Stack:** Swift 6 / SwiftUI, iOS 17+, XcodeGen, Public Sans (OFL, bundled TTFs), SF Symbols for icons, simulator screenshots for visual checks.

**Spec:** `docs/design/2026-10-23-ios-handoff/README.md` (tokens, type scale, per-screen specs) and `docs/design/2026-10-23-ios-handoff/Field Assistant iOS.dc.html` (exact values; read as text). Where the two disagree the HTML wins: the HTML is the drawing, the README the summary. A condensed value sheet of what the README omits is not in the repo; the exact values the tasks need are copied into them below.

## Global Constraints

- Font: **Public Sans** everywhere (weights 400, 500, 600, 700, bundled from the uswds/public-sans release, OFL licence file alongside). Sizes are points equal to the handoff's px; every text style scales with Dynamic Type via `relativeTo:`.
- Tokens (light): brand orange `#F26122`; accent `#C2410C`; accent tint `#FEF0E8`; charcoal `#2F3437`; dark panel `#3A4044`; muted `#5C6368`; placeholder/tertiary `#8A9196`; light-on-dark secondary `#B6BCC0`, body `#E9EBEC`, tagline `#D5D9DC`; hairline `#E1E4E6`; surface `#F4F5F6`; white `#FFFFFF`.
- Tokens (dark): ground `#2F3437`; text white; secondary `#B6BCC0`; hairline `rgba(255,255,255,.12)` for row rules and `.18` for card/button borders; raised surfaces `#3A4044`; links and kickers `#F26122`; primary fills `#F26122`; outline buttons white; halo `rgba(242,97,34,.18)`; flagged background `#3A4044` with the orange bar.
- Orange `#F26122` is reserved for the one action per screen (record, stop, submit on dark, flagged bar) and small marks; filled buttons on white use accent `#C2410C`.
- Spacing: screen padding 20 (28 on sign-in and result), row padding 14 vertical, section gap 20–24. Radii: buttons 6, cards 8, tags 4, sheet 18. Min tap height 44; primary buttons 54–56.
- Type scale (size/weight, letter-spacing): screen title 32/700 −.02em; hero 56/700 lh .98 −.03em; result number 84/700 −.04em tabular; body 15/400 lh 1.55; row title 15/500 or 17/600; label 11/700 uppercase .1em; kicker 11/700 uppercase .14em (card kickers 10/700 .12em; hero kicker 13/700 .16em); meta 12–13/400; transcript 24/500 lh 1.35 −.01em; sheet/section title 24/700 −.02em; card title 20/700.
- Icons: SF Symbols standing in for the handoff's Lucide shapes: `mic`, `stop.fill`, `camera`, `chevron.left/right/down`, `gearshape`, `checkmark`, `arrow.up.right.square`, `lock`, `wifi.slash`, `arrow.clockwise`, `magnifyingglass`; weight `.medium` for stroke 2, `.semibold` for 2.5, `.regular` for 1.5–1.75.
- Navigation stays a plain stack, no tab bar; Capture and Review push; Settings pushes from Home. Hit targets ≥ 44 everywhere; a VoiceOver label on every control; existing accessibility labels and identifiers keep their text unless a task changes the copy.
- Copy changes only where a task lists the new string; all other strings stay as they are (tests assert on them).
- Lists keep `List` where the screen is a list (project picker, drafts, RFI list, lookup picker, RFI detail) with `.listStyle(.plain)`, row insets 20 on the sides, hairline separators tinted with the token; page-like screens (sign-in, Home, Capture, Review, Result, Settings) are `ScrollView` + `VStack` with the screen padding.
- Every existing test stays green; new tests cover pure helpers only (no snapshot library). Each screen task ends with light and dark simulator screenshots saved outside the repo for the user's review.
- Tests run with `cd ios && xcodebuild test -scheme FieldAssistant -destination 'platform=iOS Simulator,name=iPhone 17' -skipPackagePluginValidation -onlyUsePackageVersionsFromResolvedFile -only-testing:FieldAssistantTests/<Class>` after `make ios-project`. Screenshots: boot the simulator, `xcrun simctl ui booted appearance light|dark`, launch the Local build, `xcrun simctl io booted screenshot <file>`.

## Review Focus

1. Dynamic Type at the largest accessibility size: every screen still scrolls and no fixed-height row clips its text. Test in Task 1 (`testTypeStylesScaleWithDynamicType`) pins the scaling; the screenshot step of each screen task includes one capture at `UIContentSizeCategory.accessibilityExtraLarge`.
2. Dark mode on a screen the handoff only draws in light (project picker, Settings, Result): text stays readable and no fixed light colour survives. Test in Task 1 (`testTokensResolveInDark`); the final task's sweep captures every screen dark.
3. A status tag or kicker for a state the handoff did not draw (RFI `open`/`closed`, draft `submitting`): it gets a defined style, never the default blue. Test in Task 2 (`testStatusTagStyleForEveryState`).
4. The record button with the microphone denied, and the stop button while finishing: disabled states use the surface/placeholder tokens, not a dimmed orange. Test in Task 5 (`testRecordControlStateMapsToDisabledStyle`).
5. The Result screen for a draft submitted without photos or without an assignee: the definition list omits the row, it does not print "0 photos" or a dash. Test in Task 7 (`testResultSummaryOmitsMissingRows`).

---

## File structure

| Path | Responsibility |
| --- | --- |
| `ios/FieldAssistant/Core/Design/Theme.swift` | `Theme.Color` adaptive colour tokens. |
| `ios/FieldAssistant/Core/Design/Typography.swift` | Public Sans registration helpers and `Theme.Text` styles. |
| `ios/FieldAssistant/Core/Design/Kicker.swift` | Uppercase kicker label (replaces the private `Kicker` in the RFI detail and `ReviewLabel`). |
| `ios/FieldAssistant/Core/Design/StatusTag.swift` | One status tag for drafts, RFIs and "Last used" (replaces `DraftStatusChip` and the list's `StatusTag`). |
| `ios/FieldAssistant/Core/Design/Buttons.swift` | `PrimaryButtonStyle`, `SecondaryButtonStyle`, `TextLinkButtonStyle`. |
| `ios/FieldAssistant/Core/Design/FormRow.swift` | Label-over-value row with optional chevron and hairline. |
| `ios/FieldAssistant/Core/Design/SearchField.swift` | The 48 pt surface search field. |
| `ios/FieldAssistant/Core/Design/Hairline.swift` | `HairlineDivider` and `hairlineBorder()` modifier. |
| `ios/FieldAssistant/Core/Design/LogoMark.swift` | The waveform mark from the asset catalog, sized. |
| `ios/FieldAssistant/Core/Design/RecordButton.swift` | Orange circle with halo, mic/stop glyph, disabled state. |
| `ios/FieldAssistant/Core/Design/OfflineBanner.swift` | Charcoal "No signal" strip. |
| `ios/FieldAssistant/Resources/Fonts/` | `PublicSans-{Regular,Medium,SemiBold,Bold}.ttf`, `LICENSE.md`. |
| `ios/FieldAssistant/Assets.xcassets` | `AppIcon`, `AccentColor`, `MarkOnLight`, `MarkOnDark`, `MarkMonoWhite`, `MarkMonoCharcoal`. |
| `ios/project.yml` | `UIAppFonts`, `ASSETCATALOG_COMPILER_APPICON_NAME`. |
| `ios/FieldAssistant/Features/RFI/DraftsListView.swift` | Handoff 1j "Drafts" screen behind Home's "All drafts". |
| `ios/FieldAssistant/Features/RFI/ResultSummary.swift` | Pure value for Result's definition list. |
| Screens restyled in place | `Core/Auth/SignInView.swift`, `Core/Scope/ProjectPickerView.swift`, `App/HomeView.swift`, `App/SettingsView.swift`, `Features/RFI/CaptureView.swift`, `Features/RFI/RFIReviewView.swift`, `Core/Review/*.swift`, `Features/RFI/RFIResultView.swift`, `Features/RFI/RecentDraftsView.swift`, `Features/RFI/RFIRecordListView.swift`, `Features/RFI/RFIRecordDetailView.swift`, `Features/RFI/AttachmentViewer.swift`, `Core/Network/APIFailureView.swift`. |
| `ios/FieldAssistantTests/Design*.swift`, `ResultSummaryTests.swift`, `RecordButtonStateTests.swift` | Tests per task. |

---

### Task 1: Design foundation: fonts, assets, tokens, type scale

**Files:**
- Create: `Core/Design/Theme.swift`, `Core/Design/Typography.swift`, `Resources/Fonts/*.ttf` + `LICENSE.md`, `Assets.xcassets` (AppIcon from `docs/design/2026-10-23-ios-handoff/logo/app-icon-1024.png`; `AccentColor` light `#C2410C` dark `#F26122`; the four `mark-*.svg` files with the `<metadata>` block stripped, "Preserve Vector Data", template rendering off)
- Modify: `ios/project.yml` (`info.properties` gains `UIAppFonts` with the four file names; app target settings gain `ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon`, `ASSETCATALOG_COMPILER_GLOBAL_ACCENT_COLOR_NAME: AccentColor`)
- Test: `FieldAssistantTests/DesignFoundationTests.swift`

**Interfaces:**
- Produces:

```swift
enum Theme {
    enum Color {   // every token adaptive: light value / dark value
        static let brand, accent, accentTint, charcoal, panel, muted, tertiary,
                   textPrimary, textSecondary, textOnDarkBody, textOnDarkSecondary, tagline,
                   hairline, hairlineStrong, surface, ground, groundDark, halo, flaggedBackground: SwiftUI.Color
    }
    enum Text {    // Font + tracking pairs, Public Sans, scaled with relativeTo
        static let screenTitle, hero, heroKicker, resultNumber, body, rowTitle, rowTitleStrong,
                   label, kicker, cardKicker, cardTitle, meta12, meta13, transcript, sectionTitle,
                   buttonLarge, buttonMedium, tagText: Theme.TextStyle
    }
    struct TextStyle { let font: Font; let tracking: CGFloat; let lineSpacing: CGFloat }
}
extension View { func textStyle(_ style: Theme.TextStyle) -> some View }   // font + tracking + lineSpacing
extension Font { static func publicSans(_ size: CGFloat, weight: Font.Weight, relativeTo: Font.TextStyle) -> Font }
```

Fonts come from the uswds/public-sans GitHub release (`fonts/ttf/PublicSans-{Regular,Medium,SemiBold,Bold}.ttf`, PostScript names `PublicSans-Regular`, `-Medium`, `-SemiBold`, `-Bold`). Light/dark pairs per the Global Constraints: `ground` white/`#2F3437`, `textPrimary` charcoal/white, `textSecondary` muted/`#B6BCC0`, `hairline` `#E1E4E6`/white .12, `hairlineStrong` `#E1E4E6`/white .18, `accent` `#C2410C`/`#F26122`, `surface` `#F4F5F6`/`#3A4044`, `halo` `#FEF0E8`/orange .18, `flaggedBackground` `#FEF0E8`/`#3A4044`; `brand`, `charcoal`, `panel`, `tertiary`, `tagline`, `textOnDark*` are fixed.

- [ ] **Step 1: Write the failing tests**

```swift
func testPublicSansIsRegistered()            // UIFont(name: "PublicSans-Bold", size: 17) != nil for all four names
func testTokensResolveInDark()               // UIColor(Theme.Color.ground).resolvedColor(with: dark trait) == #2F3437; accent → #F26122; surface → #3A4044; light: ground white, accent #C2410C
func testTypeStylesScaleWithDynamicType()    // UIFont preferred for Theme.Text.body at .accessibilityExtraLarge has pointSize > 15; at .large == 15
func testKickerTrackingIsFourteenPercent()   // Theme.Text.kicker.tracking == 11 * 0.14 (±0.01), cardKicker 10 * 0.12
```

- [ ] **Step 2: Run to verify fail, implement, run to verify pass**

Run: the test command with `-only-testing:FieldAssistantTests/DesignFoundationTests`. The asset catalog and fonts must be picked up by XcodeGen from the `FieldAssistant` folder (verify `UIAppFonts` lands in the built Info.plist).

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(ios): design foundation: Public Sans, app icon and logo assets, colour and type tokens"
```

---

### Task 2: Shared components

**Files:**
- Create: `Core/Design/Kicker.swift`, `StatusTag.swift`, `Buttons.swift`, `FormRow.swift`, `SearchField.swift`, `Hairline.swift`, `LogoMark.swift`, `RecordButton.swift`, `OfflineBanner.swift`
- Modify: `Features/RFI/RFIRecordListView.swift` (delete `StatusTag` and `RecordPalette`; use the new ones), `Features/RFI/RFIRecordDetailView.swift` (delete private `Kicker`), `Features/RFI/RecentDraftsView.swift` (delete `DraftStatusChip`; use `StatusTag`), `Core/Review/ReviewRow.swift` (`ReviewLabel` becomes a thin wrapper over `Kicker(style: .label)` or is deleted and callers updated)
- Test: `FieldAssistantTests/DesignComponentsTests.swift`

**Interfaces:**
- Produces:

```swift
struct Kicker: View { init(_ text: String, style: Kicker.Style = .section); enum Style { case section, card, hero, label, tertiary } }
struct StatusTag: View {
    enum State: Equatable { case submitted(number: String?), queued, waiting, draft, fix, lastUsed, open, closed, custom(String) }
    init(_ state: State)
    static func style(for state: State) -> StatusTag.Style   // fill/outline colours, uppercase, tracking
    static func state(for draft: LocalDraftStatus, number: String?) -> State   // waiting→.waiting, draft→.draft, submitting→.queued, submitted→.submitted(number), failed→.fix
    static func state(forRFIStatus status: String, draft: Bool) -> State       // "open"→.open, "closed"→.closed, draft→.draft, else .custom(translated)
}
struct PrimaryButtonStyle: ButtonStyle { init(onDark: Bool = false, height: CGFloat = 56) }   // accent fill (orange on dark), white 17/700, radius 6, disabled surface/tertiary
struct SecondaryButtonStyle: ButtonStyle { init(onDark: Bool = false, height: CGFloat = 54) } // 1.5 pt border charcoal (white on dark), radius 6, 15/700
struct TextLinkButtonStyle: ButtonStyle { init(color: Color = Theme.Color.accent, size: CGFloat = 14) }  // min height 44
struct FormRow<Value: View>: View { init(label: String, chevron: Bool = false, flagged: Bool = false, @ViewBuilder value: () -> Value) }  // 11/700 label, value 15, 14 pt vertical padding, top hairline, min height 60 with chevron
struct SearchField: View { init(_ placeholder: String, text: Binding<String>) }   // 48 pt, surface fill, radius 8, magnifyingglass 18 medium
struct HairlineDivider: View; extension View { func hairlineBorder(radius: CGFloat = 8, strong: Bool = false, dashed: Bool = false) -> some View }
struct LogoMark: View { enum Variant { case onLight, onDark, monoWhite, monoCharcoal }; init(_ variant: Variant, size: CGFloat) }
struct RecordButton: View { enum Glyph { case mic, stop }; init(glyph: Glyph, size: CGFloat, halo: CGFloat, isEnabled: Bool, action: () -> Void) }  // orange circle, halo ring, mic 46/.regular at 116, stop square at 76; disabled: surface fill, tertiary glyph
struct OfflineBanner: View { init(_ text: String = "No signal. Recording works; sending waits.") }  // charcoal fill, white 13/500, radius 8, wifi.slash 16 medium
```

Tag recipe: 11/700, radius 4; filled Submitted `surface`/`textPrimary` tracking .04em padding 4×8; Fix accent fill white text; Queued and Waiting accent text + 1 pt accent border padding 3×8; Draft muted text + hairline border; Last used accent-tint fill, accent text, uppercase .06em; Open = Queued's outline style; Closed = Submitted's filled style; dark: Submitted `#3A4044`/white, Queued/Waiting/Open `#F26122`, Draft `#B6BCC0` text + white .18 border.

- [ ] **Step 1: Write the failing tests**

```swift
func testStatusTagStyleForEveryState()        // every State case returns a style whose foreground is not Color.accentColor and whose text is non-empty; submitted("214") text == "#214 Submitted"
func testDraftStatusMapsToTagState()          // all five LocalDraftStatus values map as listed
func testRFIStatusMapsToTagState()            // "open" → .open, "closed" → .closed, draft true → .draft, "pending" → .custom("Pending")
func testKickerStylesUseTokens()              // Kicker.Style.section → kicker text style; .card → cardKicker
```

- [ ] **Step 2: Run to verify fail, implement, run to verify pass; build the app for the simulator**

- [ ] **Step 3: Commit**

```bash
git commit -m "feat(ios): shared design components; one status tag and kicker for every screen"
```

---

### Task 3: Sign in (1a) and Project picker (1b)

**Files:**
- Modify: `Core/Auth/SignInView.swift`, `Core/Scope/ProjectPickerView.swift`, `Core/Network/APIFailureView.swift`
- Test: existing `AuthServiceTests`, `ScopeServiceTests`, `SignInFlowUITests` (label "Sign in with Procore" unchanged)

Sign in (1a): charcoal ground (`Theme.Color.charcoal` in both modes); padding 70 top / 28 sides / 56 bottom. Top: `LogoMark(.onDark, size: 44)` + kicker "Field Assistant" (12/600 .14em `#B6BCC0`), gap 12. Centre block (gap 18): hero kicker "Procore, by voice" (13/700 .16em orange), hero "Field\nAssistant" 56/700 lh .98 −.03em white, 48×3 orange rule, tagline "Dictate an RFI on site. Review the draft. Tap once to send it to Procore." 17 lh 1.5 `#D5D9DC` max width 300. Bottom: `PrimaryButtonStyle(onDark: true)` "Sign in with Procore" 56, then note "Sign-in happens on Procore's own page. This app never sees your password." 12 centred lh 1.5 `#B6BCC0`; an error replaces the note in `#F26122`. The DEV badge stays, as a small outline tag top-right.

Project picker (1b): white ground; padding 62/20/56; company kicker 12/600 .12em accent with `chevron.down` 14 semibold (v1: one company, so the chevron is hidden when there is one company); title "Choose a project" 32/700; `SearchField("Search projects")`; rows min height 72, title 17/600, hairline between rows, `chevron.right` 18 medium tertiary; the project matching `last_project_id` shows `StatusTag(.lastUsed)`; the company screen uses the same row recipe. Footer "People, locations and spec sections download when you choose a project, so pickers work without signal." 12 tertiary centred at the bottom. Error and empty states (`APIFailureView`) use `Kicker` + body text + `SecondaryButtonStyle` "Retry"/"Sign in again" instead of `ContentUnavailableView`.

- [ ] **Step 1: Restyle both screens; keep every string and accessibility label**
- [ ] **Step 2: Run `AuthServiceTests`, `ScopeServiceTests`, build; capture light, dark and accessibility-XL screenshots of both screens**
- [ ] **Step 3: Commit** `feat(ios): sign-in and project picker on the design system`

---

### Task 4: Home (1c), Drafts list and offline banner (1j)

**Files:**
- Create: `Features/RFI/DraftsListView.swift`
- Modify: `App/HomeView.swift` (page layout; `Route.drafts` added), `Features/RFI/RecentDraftsView.swift` (row recipe; `limit` 3 on Home)
- Test: `FieldAssistantTests/HomeViewTests.swift` (unchanged strings), new `DraftsListViewTests` for the row meta string

**Interfaces:**
- Produces: `enum DraftRowMeta { static func line(feature: String, capturedAt: Date, photos: Int, now: Date, calendar: Calendar = .current) -> String }` in `RecentDraftsView.swift`; `DraftsListView(projectID: Int64, owner: Int64, onOpen: @escaping (Route) -> Void)`.

Home (1c), as a `ScrollView` page with 20 pt padding and 22 gap: header row = company kicker 11/700 .14em accent over project name 28/700 with `chevron.down` 20 semibold orange (opens the picker), and a 44 pt round settings button (hairline border, `gearshape` 20) that pushes Settings. Sync status line and Sync button sit under the project name in meta13 (same strings and behaviour as today; "Retry" and "Sync" use `TextLinkButtonStyle`). Feature grid 2×2, gap 10, cards 88 min height, padding 14, radius 8: RFI = selected (2 pt accent border, card kicker "Selected" accent, title "RFI" 20/700); Observation, Punch item, Daily log = not shipped (1 pt dashed hairline border, kicker "Coming" tertiary, title tertiary, not tappable) because v1 ships RFI only. Record block centred: `RecordButton(glyph: .mic, size: 116, halo: 10)`, label "Record an RFI" 17/700 (8 above), sub "Under a minute, start to Procore" 13 muted. "Recent" kicker (section, muted) with "All drafts" `TextLinkButtonStyle` 13/600 on the right, then up to three `RecentDraftsView` rows: min height 60, title 15/500 single line, meta 12 tertiary tabular ("RFI · 2:14 PM · 3 photos": feature, captured time (today: time; yesterday: "Yesterday"; else weekday), photo count when > 0), `StatusTag` right. Offline banner (`OfflineBanner`) at the top of the page in `.offline`, replacing the yellow bar. The "Procore RFIs" row becomes a `FormRow` with chevron under Recent.

Drafts list (1j): `DraftsListView(projectID:owner:)` pushed by "All drafts" (`Route.drafts`): title "Drafts" 32/700, the same rows with min height 72 and a third line: waiting → "Needs connection to build the draft" (accent 12/500), submitting → "Reviewed. Sends automatically, once." (muted), failed → the Procore message (accent), draft/submitted → none; footer "Each submit carries the draft's own key, so a retry after a dropped connection never posts twice." 12 tertiary. Rows route exactly as Recent rows do (`RecentDraftsView.route(for:isSynced:)`).

- [ ] **Step 1: Write the failing test** `testDraftRowMetaLine()` — `DraftRowMeta.line(feature: "rfi", capturedAt: today 14:14, photos: 3, now:)` == "RFI · 2:14 PM · 3 photos"; yesterday → "RFI · Yesterday"; no photos → no third segment.
- [ ] **Step 2: Implement; run `HomeViewTests`, `DraftsListViewTests`, `AppStateTests`; build; screenshots light/dark/XL of Home (signed in, offline) and Drafts**
- [ ] **Step 3: Commit** `feat(ios): Home and Drafts on the design system; offline banner`

---

### Task 5: Capture (1e)

**Files:**
- Modify: `Features/RFI/CaptureView.swift`
- Test: `FieldAssistantTests/RecordButtonStateTests.swift`, existing `CaptureModelTests`

**Interfaces:**
- Produces: `extension CaptureView { struct ControlState: Equatable { let stopEnabled: Bool; let buildEnabled: Bool; init(for phase: CaptureModel.Phase) } }` (use whatever enum `CaptureModel` already exposes for its recording phase; name the parameter after it).

Page padding 62/20/48, gap 16. Top bar: "Cancel" `TextLinkButtonStyle(size: 15)` 44 tall; centre: 10 pt orange dot blinking (1.1 s, opacity 1→0→1) + "Recording 0:42" 13/600 tabular; right: kicker "RFI" tertiary .12em. Transcript label "Live transcript · on device" (section kicker, muted), transcript 24/500 lh 1.35 −.01em with a 3×24 orange caret blinking (offset .3 s); placeholder text in tertiary. Photos: label "Photos · N" (kicker) and the location line 11 tertiary tabular on the right; tiles 76, radius 6, gap 10; add tile 1.5 pt dashed `#B6BCC0` (white .30 dark) with `camera` 26. Footer over a hairline, gap 14: `RecordButton(glyph: .stop, size: 76, halo: 8)` and "Build RFI" `PrimaryButtonStyle(height: 52)` 16/700, disabled (surface/tertiary) until recording stops; caption "Stop recording to build. Works without signal." 11 tertiary under the row. All existing status strings, dialogs and alerts stay.

- [ ] **Step 1: Write the failing test** `testRecordControlStateMapsToDisabledStyle()` — `CaptureView.ControlState(for: .idle/.starting/.recording/.stopped/.building/.denied)` returns stop enabled only while recording and build enabled only when stopped.
- [ ] **Step 2: Implement; run the test and `CaptureModelTests`; build; screenshots (recording with transcript, stopped with photos, denied), light/dark/XL**
- [ ] **Step 3: Commit** `feat(ios): Capture on the design system`

---

### Task 6: Review (1f) and the lookup picker

**Files:**
- Modify: `Features/RFI/RFIReviewView.swift`, `Core/Review/ReviewRow.swift`, `PairedRow.swift`, `UnresolvedRow.swift`, `ReviewFooter.swift`, `TranscriptDisclosure.swift`, `LookupPickerView.swift`
- Test: existing `RFIReviewModelTests`, `LookupPickerFilterTests`

Header: back link "‹ Capture" 14/600 accent with `chevron.left` 16 semibold (replaces the system back button text; the system gesture stays), title "Review RFI" 32/700, count tag "1 field needs you" / "N fields need you" uppercase 11/700 .06em accent on accent tint (dark: accent fill, white text), hidden when zero. Rows are `FormRow`s: plain rows padding 14, label 11/700 .1em tertiary, value 15 (Subject 16/600 lh 1.4; Question 15 lh 1.55); tappable rows min height 60 with `chevron.right` 16 medium tertiary. Paired rows (Due | Drawing, Schedule | Cost impact): two columns split by a vertical hairline, left cell padding 14 10 14 0, right 14 0 14 14. Flagged row (`UnresolvedRow`): 4 pt orange left bar, `flaggedBackground`, radius 0 8 8 0, padding 14, label accent, reason 12/600 accent on the right, candidate radio rows 48 tall (22 pt circle, 2 pt `#B6BCC0` border, filled accent when selected; name 15/600, role 12 muted), "Someone else…" 13/600 accent. Photos 64 tiles. "Field notes · original transcript" collapsed row 52 tall 14/500 muted with `chevron.down`, hairlines above and below. Sticky footer (safe-area inset, padding 12 20 44, gap 10, ground colour, hairline above): "Save draft" `SecondaryButtonStyle` flex 1, "Submit to Procore" `PrimaryButtonStyle(height: 54)` flex 1.4; the pending and saved notes keep their strings in meta13. Lookup picker: `SearchField`, rows 17/600 min height 60 with `checkmark` accent on the selected row.

- [ ] **Step 1: Restyle; run `RFIReviewModelTests` and `LookupPickerFilterTests`; build; screenshots (flagged assignee, clean, pending, offline), light/dark/XL**
- [ ] **Step 2: Commit** `feat(ios): Review and lookup picker on the design system`

---

### Task 7: Result (1g) and Settings (1h)

**Files:**
- Create: `Features/RFI/ResultSummary.swift`
- Modify: `Features/RFI/RFIResultView.swift`, `App/SettingsView.swift`
- Test: `FieldAssistantTests/ResultSummaryTests.swift`

**Interfaces:**

```swift
struct ResultSummary: Equatable {
    struct Row: Equatable { let term: String; let value: String; let tabular: Bool }
    let rows: [Row]
    init(draft: LocalDraft, projectName: String?, assigneeName: String?, now: Date)
    // rows in order, each omitted when its value is missing: Project; Assigned (assigneeName); Due (draft payload due date as "Fri, Oct 30"); Attached ("3 photos" / "1 photo", omitted at 0); Sent (updatedAt as "2:14 PM" + " · 48 s from record" when updatedAt − capturedAt < 1 h, else minutes)
}
```

Result (1g): charcoal ground both modes, padding 70/28/56, left aligned, gap 16: 56 pt orange circle with `checkmark` 28 semibold white; kicker "Created in Procore" 13/700 .16em orange; "RFI 214" 84/700 lh .95 −.04em tabular white (draft RFIs with no number show "Draft RFI" in sectionTitle); subject 20/600 lh 1.3 `#E9EBEC`; orange 48×3 rule; definition list two columns (term 14 `#B6BCC0`, value 14 white, Due and Sent tabular, row gap 8, column gap 24). Buttons (gap 10): "Open in Procore" `PrimaryButtonStyle(onDark: true)` with `arrow.up.right.square` 18 semibold; "New RFI" `SecondaryButtonStyle(onDark: true)`; "Done" `TextLinkButtonStyle(color: #B6BCC0)`.

Settings (1h): `ScrollView` page, gap 24; section kickers "Submitting", "Project", "Account" (section style, muted, 8 below). Rows padding 14, min height 64, title 15/500, sub 12 muted lh 1.4, gap 3, hairlines. Submitting: "Submit as Procore draft" / "RFIs land as drafts until you open them in Procore." with a `Toggle` tinted orange on, hairline track off (`.tint(Theme.Color.brand)`); "Photo size" / "Long edge, before upload" / value "2,500 px" 14 muted tabular. Project: project name / "<company> · lookups refreshed <relative>" (from the lookups' `fetchedAt`, newest of the project's rows; omitted when none) + "Change" 14/600 accent; "Refresh people and locations" / "Pull the latest from Procore now" with `arrow.clockwise` 18 medium accent. Account: user name / "<email> · Procore"; "Sign out" 52 tall left aligned 15/600 accent, hairlines above and below. Footer: `LogoMark(.onLight, size: 28)` + "Field Assistant 1.0 (1) · <env> · <host>" 11 tertiary tabular, gap 8. "Keep audio after submit" is not in the app and is not added.

- [ ] **Step 1: Write the failing tests**

```swift
func testResultSummaryBuildsAllRows()      // 3 photos, assignee, due, 48 s → five rows in order with "3 photos" and "2:14 PM · 48 s from record"
func testResultSummaryOmitsMissingRows()   // no photos, no assignee, no due → Project and Sent only
func testResultSummarySentMinutes()        // 3 min 5 s → "… · 3 min from record"
```

- [ ] **Step 2: Implement; run `ResultSummaryTests`; build; screenshots of Result (with and without photos) and Settings, light/dark/XL**
- [ ] **Step 3: Commit** `feat(ios): Result and Settings on the design system`

---

### Task 8: RFI list, RFI detail and attachments (extension of the handoff)

**Files:**
- Modify: `Features/RFI/RFIRecordListView.swift`, `RFIRecordDetailView.swift`, `AttachmentViewer.swift`
- Test: existing `RecordStoreTests`, `AttachmentViewerModelTests`, `RecentDraftsViewTests`

These screens have no handoff drawing; they reuse 1b/1c/1f recipes (controller's call): list = title "RFIs" 32/700 (as a large inline header, not the system large title), `SearchField("Number or subject")`, a filter row of outline tags (All, Open, Closed, Draft, Ball in my court; the active one filled charcoal with white text, like 1d's segmented pill) replacing the toolbar menu, rows min height 72 with hairlines: line 1 "#12" 13/700 tabular tertiary + `StatusTag(state(forRFIStatus:))` right; line 2 subject 15/500 two lines; line 3 meta 12 tertiary "Ball in court · Due Oct 30". Empty states use `Kicker` + body + a text link. Detail = `ScrollView` page: header kicker "RFI" tertiary, "#12" 32/700 tabular, subject 20/600, `StatusTag`; sections as kickers (Overview, Where, Impacts, Question N, Answer N, Field notes) with `FormRow`s (no chevrons); attachment rows 52 tall with a `paperclip` 16 medium, filename 15/500, caption 12 ("Needs signal" tertiary, failure accent); "Open in Procore" `PrimaryButtonStyle` at the bottom with `arrow.up.right.square`. Dark mode via tokens.

- [ ] **Step 1: Restyle; run the three test classes; build; screenshots (list with filters and search, empty "No RFI access", detail with answers and attachments), light/dark/XL**
- [ ] **Step 2: Commit** `feat(ios): RFI list and detail in the handoff language`

---

### Task 9: Dark-mode sweep, Home B and reauth notes, docs

**Files:**
- Modify: any screen with a straggler; `CLAUDE.md` (code map row for `Core/Design`; status line), `docs/superpowers/plans/2026-10-22-roadmap.md` (2b row)
- Test: full iOS suite

- [ ] **Step 1: Sweep** — launch the Local build in dark mode and walk every screen (sign-in, picker, Home signed in and offline, Drafts, Capture, Review flagged, Result, Settings, RFI list, RFI detail, attachment viewer, lookup picker); fix any fixed light colour, system blue or default font that survived; capture the dark set.
- [ ] **Step 2: Full suite** — the test command without `-only-testing`. Expected: `** TEST SUCCEEDED **`.
- [ ] **Step 3: Docs** — CLAUDE.md code map gains `ios/FieldAssistant/Core/Design` ("Design tokens, Public Sans type scale, logo assets and the shared components every screen is built from"); status line says the design pass is done; roadmap 2b row records the date and that handoff 1i (reauth sheet) moved to milestone 4 and 1d (Home B) was rejected by the handoff itself.
- [ ] **Step 4: Commit** `docs: design pass applied; reauth sheet deferred to milestone 4`
- [ ] **Step 5: Device check (Drew)** — Dev build on the iPhone: every screen in light and dark, Dynamic Type at a large setting, gloves-on tap targets on Capture. Record outcomes in the roadmap row.
