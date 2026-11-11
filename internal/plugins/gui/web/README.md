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

## History and scroll tests

`Open` supplies a recent, turn-aligned window; `History(before)` supplies the
preceding page from the same snapshot. `Chat.loadOlder` folds pages separately
from live state. Transcript rows outside the viewport/overscan are replaced by
spacers; mounted rows are measured with ResizeObserver. Scroll correction keeps
an entry ID and pixel offset through prepends and measurement cascades.

    pnpm test                 # includes paging state/cursor/race and geometry tests
    pnpm dev --port 5179      # leave running in another terminal
    pnpm test:browser         # Chromium integration, no extra npm dependencies

The browser test requires Node >=22 and `chromium` (override with `CHROMIUM`).
`HISTORY_TEST_URL` overrides the dev URL. It injects harmless Wails fixtures
before loading the real App; Send/Stop throw rather than contact a node. It
checks demand-only history loading, two prepend anchors within 2px, bounded DOM
rows while scrolling both ways, keyboard Home/End, jump-latest, resizing, and
streaming both while following and while reading older messages. The dev test
bypasses CSP only because Vite injects inline CSS; production CSP is unchanged.
For the built assets, serve `../frontend` over HTTP, then run with
`HISTORY_TEST_URL=http://127.0.0.1:PORT/ HISTORY_TEST_PRODUCTION=1`; this keeps
CSP enforcement enabled. No production assets are built by these tests.

`index.html` is the page of the native window, with the strict
Content-Security-Policy; `demo.html` is only for `pnpm dev` and is not built.
