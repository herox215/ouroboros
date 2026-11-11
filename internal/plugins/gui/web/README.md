# The window of ouro

The thread chat of `ouro gui`, in Svelte 5 and TypeScript. Vite builds it into
`../frontend`, which `go:embed` puts into the binary.

    pnpm install         # once
    pnpm dev             # /demo.html: a scripted thread to look at
    make frontend        # build into ../frontend (committed)
    pnpm run check       # svelte-check and TypeScript
    pnpm test            # Vitest: store, markdown and components

The window talks to the Go side through the bindings of `Window` (see
`window.go`): `InitialState`, `Open`, `Send`, `Stop`. What the thread tells
comes as Wails events (`ouro:events`, `ouro:ended`, `ouro:restarting`); see
`src/lib/bridge.ts` and `src/lib/wire.ts`.

`index.html` is the page of the native window, with the strict
Content-Security-Policy; `demo.html` is only for `pnpm dev` and is not built.
