# gorc — device connector front end

React + Vite UI for the Go Fiber device connector. See the
[project README](../README.md) for architecture, supported devices and how to
connect real hardware.

```sh
npm install
npm run dev     # http://localhost:5173, proxies /api and /ws to :8080
```

The backend must be running (`make dev-api` from the repo root) — the browser
never touches hardware here, it renders a websocket stream.

| Script | |
| --- | --- |
| `npm run dev` | dev server with the backend proxy configured in `vite.config.ts` |
| `npm run build` | type-check and bundle to `dist/` |
| `npm run lint` | oxlint |
