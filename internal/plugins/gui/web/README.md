# The window of ouro

The thread chat of `ouro gui`, in Svelte 5 and TypeScript. Vite builds it into
`../frontend`, which `go:embed` puts into the binary.

    pnpm install         # once
    pnpm dev             # /demo.html: a scripted thread to look at
    make frontend        # build into ../frontend (committed)
    pnpm run check       # svelte-check and TypeScript
    pnpm test            # Vitest: store, markdown and components

The window talks to the Go side through the bindings of `Window` (see
`window.go`): `InitialState`, `Open`, `History`, `Send`, `Stop`. What the thread tells
comes as Wails events (`ouro:events`, `ouro:ended`, `ouro:restarting`); see
`src/lib/bridge.ts` and `src/lib/wire.ts`.

Beside the thread the window shows the board (see `board.go`): `Board` returns
its lanes with a card a ticket, `Ticket` one ticket in full. Nothing of it
comes as an event: `src/lib/board.svelte.ts` asks again every few seconds
while the board is shown. `pnpm dev` has a scripted board too.

Beside the board it lists the sessions (`sessions.go`), the agents of the
profile (`agents.go`: `Agents`, `UpdateAgent`, `CreateAgent`, `DeleteAgent`)
and the scheduled jobs (`jobs.go`: `AgentJobs`), read again while they are shown.

At the foot of the sidebar are the options: the theme of the window. A theme is
ten colors and whether it is dark (`src/lib/themes.svelte.ts`); the rest of the
palette of `app.css` is mixed from them and set as custom properties on the
root. "System" sets nothing, so `app.css` follows the desktop. The built-in
themes are well-known editor schemes; the owner's own themes, and which one is
worn, are kept in `localStorage` (`ouro.theme`, `ouro.themes`), so each window
or browser keeps its own. No binding is involved.

## History and scroll tests

`Open` supplies a recent, turn-aligned window; `History(before)` supplies the
preceding page from the same snapshot. `Chat.loadOlder` folds pages separately
from live state. Transcript rows outside the viewport/overscan are replaced by
spacers; mounted rows are measured with ResizeObserver. Scroll correction moves
the scroll position by what changed above the row the view shows, through
prepends and measurement cascades, and does not write it otherwise: a write ends
a wheel scroll that is on its way, as each piece of an answer streaming in below
would. Scrolling up stops following the end until the owner scrolls down to it.

    pnpm test                 # includes paging state/cursor/race and geometry tests
    pnpm dev --port 5179      # leave running in another terminal
    pnpm test:browser         # Chromium integration, no extra npm dependencies

The browser test requires Node >=22 and `chromium` (override with `CHROMIUM`).
`HISTORY_TEST_URL` overrides the dev URL. It injects harmless Wails fixtures
before loading the real App; Send/Stop throw rather than contact a node. It
checks demand-only history loading, two prepend anchors within 2px, bounded DOM
rows while scrolling both ways, keyboard Home/End, jump-latest, resizing,
streaming both while following and while reading older messages, and a wheel
step away from the end, and back to it, while an answer streams in. The dev test
bypasses CSP only because Vite injects inline CSS; production CSP is unchanged.
For the built assets, serve `../frontend` over HTTP, then run with
`HISTORY_TEST_URL=http://127.0.0.1:PORT/ HISTORY_TEST_PRODUCTION=1`; this keeps
CSP enforcement enabled. No production assets are built by these tests.

`index.html` is the page of the native window, with the strict
Content-Security-Policy; `demo.html` is only for `pnpm dev` and is not built.
