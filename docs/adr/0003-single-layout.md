# Publish one complete sidebar layout and let tmux clip it from the bottom

Each update renders one complete layout and writes it to a single global option, `@tos_sb_main`. The `side-status-format` is always `#{@tos_sb_main}` and does not check height. When the content exceeds the client's available height, tmux clips it from the bottom and may cut through a group heading or tab row. The sidebar does not display “... N more tabs” or add another hint.

- The top summary still counts every tab, including off-screen tabs. It is the only indication that more content exists.
- Notice wrapping, the no-tabs and empty-list messages, and click-to-jump range wrapping are unchanged.
- Keep `render.OptPrefix = "@tos_sb_"`. Keep the existing cleanup of old options with that prefix on the first update and cleanup on stop. No separate migration is needed; users can restart tmux.
- `render.FitView` and `Fit` no longer take a height or return a hidden count. Remove `RowMore`, `Row.Hidden`, `MinAvail`, `Published.Select`, and the logic that tried layouts one at a time to reserve a footer row.

## Why change it?

The previous approach (#3 and #10) rendered a separate layout for every available height that changed what fit and published each as `@tos_sb_<avail>`. `Format()` used nested conditions based on `client_height` minus the number of status lines. Layout cost grew roughly with the square of the total number of rows: for 30 groups with 10 tabs each, one update took about 515 ms and wrote about 6 MB to tmux.

## Trade-offs

- Gains: one layout per update, with cost growing linearly with the row count; no height checks in the format; one option.
- Losses: no “N more tabs” hint for off-screen content, and clipping can cut through group headings or tab rows rather than showing only complete tab rows. Users rely on the top summary for off-screen status.
- Out of scope: scrolling, pagination, other hints for hidden content, performance benchmarks, and regression tests to prevent a return to per-height layouts.

## Superseded requirements

The excerpts below are translated from the original issues.

- #1, line 60: “When height is limited, keep the top summary ... show only the complete tab rows that fit, with ‘... N more tabs’ at the bottom.”
- #1, user story 41: “When height is limited, see how many tabs are not shown.”
- #1, acceptance criterion 20: “When height is limited, keep only complete tab rows; count hidden tabs, including tabs in entirely hidden groups ...”
- #1, “Do not clip everyone's list to the shortest client,” and related passages: all clients now use the same complete layout, and tmux clips it to each client's own height. It still does not use the shortest client's height, but it no longer renders a separate layout for each height.
- #1, user story 42: “Preserve the summary and complete rows at each client's size.” Only the summary is now guaranteed; clipping can cut through a row.
- #1, line 165: “The agreed UI clips at full-row boundaries and shows the number of hidden tabs.” tmux now clips from the bottom, and there is no hidden count.
- #1, live acceptance test: “With multiple attached clients of different heights or sizes on one server, verify per-client clipping, complete rows, the overall summary, and the remaining tab count.” The test now only verifies that each client sees the start of the same layout and the overall summary. It no longer checks for complete rows or a remaining tab count.
- #3, conclusion: “Each attached client clips to its own height; ... the N in ‘... N more tabs’ counts tabs.” The shared global state and per-client presentation still apply, but tmux now handles the clipping.

Leave the body of issue #1 unchanged; this ADR takes precedence.

## Relationship to other decisions

This does not conflict with ADR-0002: each row has its own click-to-jump range, so visible rows retain their ranges after clipping.
