# Design

The look of regrow's scan page (`app/Resources/page.html`): the findings, the plan, the confirm step, the run and its result, shown in the menubar app's window. It is a reading list: the consequence as a headline, the safe findings folded into one line, and each decision that is the owner's as a short text with its size drawn beside it.

## Tokens

```css
:root {
  --paper: #fafbf8; --panel: #eff2ec; --ink: #18211b; --ink-2: #56635a; --ink-3: #88948b;
  --line: #d9dfd6; --line-strong: #c3ccc0;
  --accent: #1e6a45; --accent-soft: #ddebdf; --on-accent: #ffffff;
  --caution: #9a5b00; --danger: #b3261e; --on-danger: #ffffff;
  --taken: #7b887f; --purge: #b3beb5; --track: #e2e7df;
  --b-now: #3280de; --b-trash: #039868; --b-export: #c2780f; --b-shown: #4a3ba8; --b-macos: #da6e97;
  --serif: ui-serif, "New York", Georgia, serif;
  --sans: system-ui, -apple-system, BlinkMacSystemFont, sans-serif;
  --mono: ui-monospace, "SF Mono", Menlo, monospace;
}
/* dark */
  --paper: #131714; --panel: #1b211c; --ink: #e6ece5; --ink-2: #9daa9f; --ink-3: #6e7a71;
  --line: #2b332d; --line-strong: #3c463e;
  --accent: #78c79a; --accent-soft: #1f3327; --on-accent: #0e1a12;
  --caution: #e0a84a; --danger: #f08a80; --on-danger: #2a0f0c;
  --taken: #8d9a90; --purge: #56625a; --track: #2a322c;
  --b-now: #4795f4; --b-trash: #30ac7d; --b-export: #d98f2e; --b-shown: #6456b5; --b-macos: #b53266;
```

The page follows `prefers-color-scheme`; `data-theme="light|dark"` on `<html>` overrides it for a screenshot.

## Colour

- `--accent` is the one action colour: links, the primary button, the progress bar, the current step, a tick box.
- `--danger` marks what cannot be undone or did not work: the run button and its tick box, the "Not scanned" heading, error text. `--caution` colours an icon only, never a fill.
- The five `--b-*` colours each stand for one total, on every mark of that total and on nothing else: its swatch, its rule, its size bars, its meter segment. `--b-now` frees now, `--b-trash` after the Trash is emptied, `--b-export` kept as tarballs until they are deleted (a docker volume's export; it appears only when a scan or plan holds one), `--b-shown` shown only, `--b-macos` macOS-managed. They hold apart for every pair, also under protanopia and deuteranopia, only at these values: a change to one is checked against the other four in both themes. `--b-export` was added by that check: simulated with the Machado matrices, its CIE76 distance to each other colour is at least 36 in the light theme and 39 in the dark one under protanopia and deuteranopia, where the closest pair of the other four is 13.8 (`--b-trash` and `--b-macos`, deuteranopia, light).
- `--taken`, `--purge` and `--track` draw the disk meter: taken, purgeable, free.
- Every surface is flat `--paper`. `--panel` fills only the tray and a notice; `--accent-soft` only a row that arrived a moment ago, a finished step, and the plan's group whose action is running.

## Type

Serif for what is read as a sentence (the headline, section and finding titles, a finding's story, the plan's lead paragraph); sans for labels, figures and controls; mono at 12px for commands, paths and error text.

| | |
|---|---|
| Headline | serif 600 23/1.2; 28 on the confirm step |
| Section title, the name in the top bar | serif 600 17 |
| Finding title / plan action title | serif 600 16 / 15 |
| Story | serif 400 14/1.45, at most 62ch |
| Body | sans 13/1.4 |
| Secondary line | sans 12.5 in `--ink-2`; 12 for the smallest |
| A total / a row's size | sans 650 20 / 15, tabular figures |
| Button | sans 650 13.5 |

A size is the figure and its unit set apart: the unit at regular weight in `--ink-2`, one step smaller.

## Layout

- Three rows fill the window: a 42px top bar, the scrolling page, a 60px tray. 32px of side padding. Drawn for 900×640.
- Rows are separated by 1px `--line`; a section header and the tray's top edge by `--line-strong`.
- A finding row is a grid: 28px for the tick box, the text, and a 246px size column (a 148px bar, a 10px gap, an 88px figure). A row with nothing to tick has no tick-box column.
- Radius: 8px on buttons, notices and highlighted rows; 2 to 3px on bars and swatches.
- Icons are inline SVG, 14px, 1.5px stroke in `currentColor`.
- The page behaves as an app: default cursor, no text selection, except commands, paths and error text, which can be selected. Focus is a 2px `--accent` outline, offset 2px.

## The page, top to bottom

1. **Top bar**: the name, the scan's status in words, one action on the right ("Scan again"; while scanning, a progress bar and "Stop"). Empty on the plan and confirm steps.
2. **Headline**: with a forecast, when the disk fills at the recent pace; without one, free space of the total, and a line under the meter saying why there is no forecast.
3. **Disk meter**: 6px, three segments with 2px gaps, then the figures in words: free, purgeable, swap.
4. **Four totals**, side by side, and a fifth, the volume exports, only when the scan holds a docker volume: a 4px rule filled against the largest of them, the figure, a swatch and a label saying when the space comes back. They are never added up, and a line under them says so. While a scan runs each reads "pending".
5. **Notice** (Full Disk Access off): one `--panel` row, a lock icon, a sentence, its action on the right.
6. **Sections**, in this order, each only when it has rows: Not scanned, Safe to clear, Your call, Only shown. A header is the title, a count in words, and on the right the bar scale or a link.
7. **Tray**: the three steps (Findings, Plan, Confirm) as numbered circles, a sentence on where things stand, the next action as the one filled button. On the findings, the sentence counts what is ticked, the skips its plan would have, and what that frees, by total.

## Findings

- **Safe to clear** is folded into one ticked line: the six largest by name and size, "and N smaller ones", then one sentence. "Show each one" opens it into compact sans rows; "Fold into one line" closes it.
- **Your call**: the title with its item count, the regeneration story, then one line with what it costs to bring back and what runs. On the right the bar, the size, when the space comes back, and when it was last used.
- **Only shown** rows carry the rule's note in place of the cost line.
- A lower bound is written "≥ " before the figure. A row nothing could be read from has no bar and no figure: a lock, "unreadable", and what it needs.
- A row whose folder also holds other rows draws and writes its own bytes, and says under them how much the folder holds.
- A rule that failed is in "Not scanned": its title, the error in mono `--danger`, and no size.
- **A finding's items**: a finding with more than one item gives its count beside the title as an `--accent` link ("10 of 12 folders"; "4 items" where nothing can be picked one by one). It opens into compact rows under the finding's text: a tick box only where the item can be picked, the path in mono with `~` (or the item's label), its last use as a secondary line, and on the right a bar and size on the list's shared scale. An item nothing could be read from shows the lock and "unreadable".
- A tick box exists only where the engine handed the row its atoms; the page has no rule of its own about what may be deleted.

## Size bars

A 6px bar on a 1px `--line-strong` hairline the full width of its column; the fill starts at the left in the total's colour, its right end rounded, at least 2px wide. All bars of a list share one scale, a full bar being the largest row, and the first section header that draws a bar says so. A bar always sits beside its written size.

## Plan and confirm

- **Plan**: a headline with the number of actions; one paragraph, with swatches, on what frees as commands run and has no undo and what goes to the Trash; the disk meter as it will stand, with "free once the commands end". Then "Skipped" with each reason, before "In the order they run": a sequence number (a range for a group), the title, the first command or path in mono with "and N more like it" (a link that opens the group to every line), what runs first and what else is removed, and on the right the bar, the size, and "no undo" or "undo restores".
- **Confirm**: one centred column, at most 560px: the question as the headline, a list pairing each figure with a sentence, then a tick box that unlocks the `--danger` button. Return never runs the plan.
- **The run**: the plan's list stays, every line of each group shown and marked by a 14px icon: a ring waiting, a filled dot running, a tick done in `--accent`, a cross failed in `--danger` with its error in mono `--danger` under it, a dash skipped. The running group takes `--accent-soft`, and its size column says in words what became of its lines ("2 done, running"). The top bar holds the progress and "Stop after this action"; the tray has no button.
- **The result**: a headline counting what was done, failed and skipped; a paragraph with swatches on what is free now, what is in the Trash and what is in regrow staging; `regrow undo <run id>` in selectable mono whenever anything went to the Trash or staging, `regrow` being the command the engine's `hello` names (an app's engine by its path), quoted for a shell. "Failed" comes first, as a `--danger` section, then the list as it ran, marked. "Scan again" is the one way on.

## Wording

- Sizes as `engine.HumanBytes` prints them.
- Say when space comes back, in plain words: "frees now", "after the Trash is emptied", "kept as tarballs until you delete them", "shown only", "macOS reclaims it by itself".
- Ages: today, yesterday, N days ago, N months ago.
- The home folder is written `~`, except in a command the owner is told to paste into Terminal and in a tool's own error text, which stay as written. A command or path wraps at a space or after a slash, never inside a name; the exact command is in the element's `title`.

## The menu

The menu bar menu is native: system controls and text only, in the order the owner reads it (figures, the last check, autotrim, Full Disk Access, staging, the login environment when it could not be read, "Scan…", "Quit regrow"). None of the page's colours or type reaches it.

## Checking a change

`just page-check` draws every state through WebKit, the engine the app's web view uses, at 900×640, from the recorded engine transcript, and writes a PNG of each: results, plan and confirm in light and dark, every other state in light. Hold each against this file.
