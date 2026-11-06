import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn, spawnSync } from "node:child_process";

const directory = await mkdtemp(join(tmpdir(), "hotmouse-ui-e2e-"));
const binary = join(
  directory,
  process.platform === "win32" ? "hotmouse.exe" : "hotmouse",
);
const cwd = fileURLToPath(new URL("../../backend/", import.meta.url));
const build = spawnSync("go", ["build", "-o", binary, "./cmd/hotmouse"], {
  cwd,
  stdio: "inherit",
});
if (build.status !== 0) {
  await rm(directory, { recursive: true, force: true });
  process.exit(build.status || 1);
}
const child = spawn(
  binary,
  [
    "serve",
    "--addr",
    `127.0.0.1:${process.env.HOTMOUSE_E2E_BACKEND_PORT || 18080}`,
    "--db",
    join(directory, "test.db"),
    "--auth",
    "required",
  ],
  { cwd: directory, stdio: "inherit" },
);
let stopping = false;
for (const signal of ["SIGTERM", "SIGINT"])
  process.on(signal, () => {
    stopping = true;
    child.kill("SIGTERM");
  });
child.on("exit", async (code) => {
  await rm(directory, { recursive: true, force: true });
  process.exit(stopping ? 0 : code || 0);
});
child.on("error", async (error) => {
  process.stderr.write(`${error.message}\n`);
  await rm(directory, { recursive: true, force: true });
  process.exit(1);
});
