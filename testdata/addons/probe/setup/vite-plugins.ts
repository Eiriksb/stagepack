// A test addon: Slidev loads setup/vite-plugins.ts from every addon root, so GET /__probe-addon
// answering "ok" shows that Slidev loaded this addon. It imports nothing, as nothing resolves
// from this folder.
interface Server {
  middlewares: { use(path: string, fn: (req: { method?: string }, res: { setHeader(k: string, v: string): void, end(s: string): void }, next: () => void) => void): void }
}

export default function probeVitePlugins() {
  return [{
    name: 'stagepack-probe-addon',
    configureServer(server: Server) {
      server.middlewares.use('/__probe-addon', (req, res, next) => {
        if (req.method !== 'GET')
          return next()
        res.setHeader('Content-Type', 'text/plain')
        res.end('ok')
      })
    },
  }]
}
