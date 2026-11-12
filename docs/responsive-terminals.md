# Responsive terminal experiment

ntfuzz now exercises three separate responsibilities. This is an internal
prototype, not a new public Nimble Terminal package.

| Layer | Current implementation | Candidate platform surface |
| --- | --- | --- |
| Browser host | `internal/booba/viewport.mjs`, shell CSS | Booba opt-in viewport attachment with `fit` and `dispose` |
| TUI space allocation | `internal/tui/responsive.go` | Cell width/height budgets and wrapped-record pagination; prove in another app before extraction |
| Application priorities | Compact fleet, revision records, simplified graphs | Stays in ntfuzz; the platform cannot decide what a recovery result means |

## Existing contract

Booba already fits its terminal container, observes element resize and device
pixel ratio, and forwards columns, rows, and pixel dimensions through the existing
session. ntfuzz consumes terminal resize messages. No new wire protocol, user-agent
sniffing, reconnect-on-rotation, or browser-specific application mode is needed.

Terminal Actions already expose the TUI key map as buttons. This change adds the
revision paging bindings to that map. The OSC status envelope has a 2 KiB decoded-message budget. Adding paging
exceeded it when all key aliases and hints were included; ntfuzz now publishes
one preferred gesture per button, leaving aliases in terminal help. Tests use
the complete status summary and a realistic session identifier. The narrow host presents a horizontally
scrollable action rail with 44px minimum targets; tapping an action does not ask
for the keyboard. The existing Keyboard action remains explicit.

## Host responsibilities

The helper coalesces visual-viewport resize/scroll and window orientation/resize
notifications into one animation frame. It sizes the shell before asking the
terminal to fit. CSS accounts for safe-area insets. API links are an expandable
section, and touch devices do not automatically focus terminal input on connect.

Pinch zoom keeps native magnification rather than repeatedly changing the grid.
The helper removes listeners and restores owned CSS properties on disposal.
Persisted back/forward-cache pages are not disposed on `pagehide`.

Background: [VisualViewport](https://developer.mozilla.org/en-US/docs/Web/API/VisualViewport)
and [viewport-relative CSS lengths](https://developer.mozilla.org/en-US/docs/Web/CSS/length).
The actual mobile Safari keyboard and browser-toolbar behavior still needs a
physical-device check; Chrome emulation is not a substitute.

## Application responsibilities

Below 80 columns, or below 24 rows, Fleet, Crashes, and Stats use compact presentations.
Desktop tables remain unchanged. Width chooses presentation; height budgets the
content. Font size is not reduced to squeeze in a desktop dashboard. Compact Fleet
and Stats retain read-only snapshot freshness.

Revisions prioritizes observation/sync time, run outcomes, saved-input replay
pass/fail/blocked/pending counts, and tested targets. It replaces the chart/table
combination with wrapped records. Whole records stay together when possible;
records taller than the viewport continue on subsequent pages. Previous/next
page controls are available as Terminal Actions. Wide screens retain the revision
chart and table.

Compact graphs show one series: execution rate, coverage percentage, crash runs,
or open crash backlog. Summaries retain companion counters. Burndown shows open,
new, reopened, and resolved totals, with an estimated-history note when applicable.
Text and Kitty share chart dimensions and series selection. Short viewports
suppress the chart before consuming the space needed for the summary. Switching
to compact revision records releases Kitty graphics resources.

Compact Crashes presents one wrapped record at a time, retaining the report path,
blamed source, and stack summary. The web shell also links to a server-rendered
HTML crash index for direct report navigation and screen-reader access.

Remaining application work: target detail still uses the existing layout. Long
fleet target names are abbreviated in the compact list. A general responsive
widget package should wait until that screen and another application demonstrate
which constraints are actually reusable.

## Validation

- Node tests cover keyboard/toolbar changes, coalescing, pinch zoom, fallback, and
  cleanup; run `node --test internal/booba/viewport.test.mjs`.
- TUI tests exercise all five queries across 32/40/48/79/120 columns and
  12/20/23/30/45 rows, retaining recovery data through wrapped pages.
- Kitty tests exercise narrow/short/wide transitions and revision suppression.
- Existing WebSocket tests verify resizing an established terminal session.
- Full Go race suite and vet remain the release gate.

## Explicit touch keyboard (phone feedback)

Physical-phone testing found that suppressing initial autofocus was insufficient:
ntwebterm's canvas `touchend` listener explicitly focuses its hidden textarea even
after swiping. The ntfuzz host now installs `touch_keyboard.mjs` after terminal
initialization. Touch/pen input makes the textarea read-only with `inputmode=none`
and clears the host's contenteditable surface before renderer focus runs. It does
not cancel or stop touch events, so existing scroll/mouse handling still receives
them. Keyboard restores editing and refocuses synchronously from the button
gesture. Returning to the terminal surface dismisses typing mode; a genuine mouse
pointer restores normal desktop behavior. Compatibility mouse events do not
reenable editing. Disposal restores the original attributes.

This is a host adapter to the current ntwebterm DOM. A future Booba/ntwebterm
`touchFocus: explicit` option should own this policy directly, including renderer
swaps, rather than requiring every host to inspect its textarea. The change only
addresses keyboard activation; it does not change the TUI's scrolling semantics.

Tests cover touch tap/swipe propagation, renderer-style focus, explicit keyboard,
hybrid mouse/touch transitions, cleanup, and serving the embedded helper. Physical
iPhone Safari confirmation is still needed for this follow-up.
