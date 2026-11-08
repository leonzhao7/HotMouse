import http from "node:http";
import https from "node:https";
import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { extname, resolve, sep } from "node:path";

const root = fileURLToPath(new URL("./dist/", import.meta.url));
const backend = new URL(
  process.env.HOTMOUSE_BACKEND_URL || "http://127.0.0.1:8080",
);
if (
  !["http:", "https:"].includes(backend.protocol) ||
  backend.pathname !== "/" ||
  backend.search ||
  backend.hash
)
  throw new Error("HOTMOUSE_BACKEND_URL must be an HTTP(S) origin");
const port = Number(process.env.PORT || 4173);
const host = process.env.HOST || "127.0.0.1";
const types = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".woff2": "font/woff2",
};
try {
  await stat(resolve(root, "index.html"));
} catch {
  throw new Error("Run npm run build before npm start.");
}
const server = http.createServer(async (request, response) => {
  let pathname;
  let requestURL;
  try {
    requestURL = new URL(request.url, "http://localhost");
    pathname = decodeURIComponent(requestURL.pathname);
  } catch {
    response.writeHead(400).end("Invalid URL");
    return;
  }
  if (/^\/(api|projects)(\/|$)/.test(pathname) || pathname === "/healthz") {
    const target = new URL(backend);
    target.pathname = requestURL.pathname;
    target.search = requestURL.search;
    const upstream = (backend.protocol === "https:" ? https : http).request(
      target,
      {
        method: request.method,
        headers: { ...request.headers, host: backend.host },
      },
      (result) => {
        response.writeHead(result.statusCode, result.headers);
        result.pipe(response);
        result.on("error", () => response.destroy());
      },
    );
    upstream.on("error", (error) => {
      process.stderr.write(`${JSON.stringify({
        time: new Date().toISOString(), level: "ERROR", component: "frontend",
        event: "proxy.error", method: request.method,
        path: requestURL.pathname, code: error.code || "UNKNOWN",
        backend: backend.origin,
      })}\n`);
      if (!response.headersSent)
        response.writeHead(502, { "Content-Type": "application/json" }).end(
          JSON.stringify({
            detail: "无法连接 HotMouse 后端，请检查服务和代理地址。",
          }),
        );
      else response.destroy();
    });
    request.on("aborted", () => upstream.destroy());
    response.on("close", () => {
      if (!response.writableFinished) upstream.destroy();
    });
    request.pipe(upstream);
    return;
  }
  if (!["GET", "HEAD"].includes(request.method)) {
    response.writeHead(405).end("Method not allowed");
    return;
  }
  const path = resolve(root, `.${pathname === "/" ? "/index.html" : pathname}`);
  if (!path.startsWith(root.endsWith(sep) ? root : root + sep)) {
    response.writeHead(403).end("Forbidden");
    return;
  }
  try {
    const info = await stat(path);
    if (!info.isFile()) {
      response.writeHead(404).end("Not found");
      return;
    }
    response.writeHead(200, {
      "Content-Type": types[extname(path)] || "application/octet-stream",
      "Content-Length": info.size,
      "Cache-Control": pathname.startsWith("/assets/")
        ? "public, max-age=31536000, immutable"
        : "no-cache",
      "X-Content-Type-Options": "nosniff",
    });
    if (request.method === "HEAD") response.end();
    else
      createReadStream(path)
        .on("error", () => response.destroy())
        .pipe(response);
  } catch {
    response.writeHead(404).end("Not found");
  }
});
server.listen(port, host, () =>
  process.stdout.write(
    `HotMouse frontend: http://${host}:${port} → ${backend.origin}\n`,
  ),
);
for (const signal of ["SIGINT", "SIGTERM"])
  process.on(signal, () => {
    process.stderr.write(`${JSON.stringify({
      time: new Date().toISOString(), level: "INFO", component: "frontend",
      event: "frontend.shutdown", signal,
    })}\n`);
    server.close(() => process.exit(0));
  });
